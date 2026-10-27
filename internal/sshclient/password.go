package sshclient

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"

	"golang.org/x/crypto/ssh"
)

var ErrPasswordChange = errors.New("remote password change failed; check the current password and the server password policy")
var ErrCurrentPasswordRequired = errors.New("current remote user password is required")
var ErrPasswordChangeUncertain = errors.New("password change timed out or was interrupted; verify the remote password before retrying")

// ChangePassword runs passwd for the authenticated user, never interpolating a
// password into a command. Only a separate PTY is closed on cancellation.
func ChangePassword(ctx context.Context, client *ssh.Client, current, next string) error {
	if !validPassword(next) || (current != "" && !validPassword(current)) {
		return errors.New("password must not contain control characters")
	}
	if ctx.Err() != nil {
		return ErrPasswordChangeUncertain
	}
	// Channel opening and SSH request acknowledgements have no context-aware
	// API. Bound the entire operation, including NewSession, for the caller.
	// A late channel is closed without running passwd; closing the parent SSH
	// connection also releases a pending channel-open goroutine.
	done := make(chan error, 1)
	go func() { done <- changePassword(ctx, client, current, next) }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ErrPasswordChangeUncertain
	}
}

func changePassword(ctx context.Context, client *ssh.Client, current, next string) error {
	session, err := client.NewSession()
	if err != nil {
		return ErrPasswordChange
	}
	defer session.Close()
	if ctx.Err() != nil {
		return ErrPasswordChangeUncertain
	}
	stop := context.AfterFunc(ctx, func() { _ = session.Close() })
	defer stop()
	stdin, err := session.StdinPipe()
	if err != nil {
		return ErrPasswordChange
	}
	conversation := &passwordConversation{input: stdin, current: current, next: next, abort: func() { _ = session.Close() }}
	session.Stdout, session.Stderr = conversation, conversation
	if err := session.RequestPty("dumb", 24, 80, ssh.TerminalModes{ssh.ECHO: 0}); err != nil {
		return errors.New("server does not permit password changes using a PTY")
	}
	if ctx.Err() != nil {
		return ErrPasswordChangeUncertain
	}
	if err := session.Run("LC_ALL=C passwd"); err != nil {
		if ctx.Err() != nil {
			return ErrPasswordChangeUncertain
		}
		if conversation.err != nil {
			return conversation.err
		}
		return ErrPasswordChange
	}
	if conversation.err != nil || conversation.sentNew != 2 {
		return ErrPasswordChange
	}
	return nil
}

func validPassword(value string) bool {
	if value == "" || len(value) > 1024 {
		return false
	}
	for _, r := range value {
		if r < 32 || r == 127 {
			return false
		}
	}
	return true
}

// passwd prompts may arrive split across SSH packets. Never retain or return
// raw remote output, which may contain sensitive data on a misconfigured host.
type passwordConversation struct {
	mu            sync.Mutex
	input         io.Writer
	current, next string
	pending       string
	sentCurrent   bool
	sentNew       int
	err           error
	abort         func()
}

func (p *passwordConversation) Write(data []byte) (n int, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	defer func() {
		// A rejected/unknown prompt must also stop passwd, otherwise Session.Run
		// waits for a process that is still waiting for input.
		if err != nil && p.abort != nil {
			p.abort()
		}
	}()
	if p.err != nil {
		return 0, p.err
	}
	p.pending += string(data)
	if len(p.pending) > 8192 {
		p.err = ErrPasswordChange
		return 0, p.err
	}
	// A prompt ends in ':'; keep only the current line when recognizing it.
	if !strings.HasSuffix(strings.TrimSpace(p.pending), ":") {
		return len(data), nil
	}
	line := strings.ToLower(p.pending)
	if i := strings.LastIndexAny(line, "\r\n"); i >= 0 {
		line = line[i+1:]
	}
	response := ""
	switch {
	case strings.Contains(line, "password") && (strings.Contains(line, "current") || strings.Contains(line, "old")):
		if p.current == "" {
			p.err = ErrCurrentPasswordRequired
			return 0, p.err
		}
		if p.sentCurrent || p.sentNew != 0 {
			p.err = ErrPasswordChange
			return 0, p.err
		}
		p.sentCurrent = true
		response = p.current
	case strings.Contains(line, "password") && (strings.Contains(line, "retype") || strings.Contains(line, "repeat") || strings.Contains(line, "again")):
		if p.sentNew != 1 {
			p.err = ErrPasswordChange
			return 0, p.err
		}
		p.sentNew = 2
		response = p.next
	case strings.Contains(line, "password") && strings.Contains(line, "new"):
		if p.sentNew != 0 {
			p.err = ErrPasswordChange
			return 0, p.err
		}
		p.sentNew = 1
		response = p.next
	default:
		p.err = ErrPasswordChange
		return 0, p.err
	}
	p.pending = ""
	if _, err := io.WriteString(p.input, response+"\n"); err != nil {
		p.err = err
		return 0, err
	}
	return len(data), nil
}
