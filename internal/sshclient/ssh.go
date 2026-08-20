package sshclient

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
	"golang.org/x/term"

	"github.com/zzqjlcn/xsh/internal/config"
)

type HostResolver func(name string) (*config.SSHHost, bool)
type PasswordResolver func(name string) (string, error)

type Connection struct {
	Client *ssh.Client
	jump   *ssh.Client
}

func Dial(host config.SSHHost, password string, resolve HostResolver, resolvePassword PasswordResolver) (*Connection, error) {
	client, jump, err := dial(host, password, resolve, resolvePassword)
	if err != nil {
		return nil, err
	}
	return &Connection{Client: client, jump: jump}, nil
}

func (c *Connection) Close() error {
	err := c.Client.Close()
	if c.jump != nil {
		if jumpErr := c.jump.Close(); err == nil {
			err = jumpErr
		}
	}
	return err
}

func Connect(host config.SSHHost, password string, resolve HostResolver, resolvePassword PasswordResolver) error {
	connection, err := Dial(host, password, resolve, resolvePassword)
	if err != nil {
		return err
	}
	defer connection.Close()

	session, err := connection.Client.NewSession()
	if err != nil {
		return err
	}
	defer session.Close()
	session.Stdin, session.Stdout, session.Stderr = os.Stdin, os.Stdout, os.Stderr

	fd := int(os.Stdin.Fd())
	width, height, err := term.GetSize(fd)
	if err != nil {
		width, height = 120, 40
	}
	termType := os.Getenv("TERM")
	if termType == "" {
		termType = "xterm-256color"
	}
	modes := ssh.TerminalModes{ssh.ECHO: 1, ssh.TTY_OP_ISPEED: 14400, ssh.TTY_OP_OSPEED: 14400}
	if err := session.RequestPty(termType, height, width, modes); err != nil {
		return err
	}

	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return err
	}
	defer term.Restore(fd, oldState)
	if err := session.Shell(); err != nil {
		return err
	}

	done := make(chan struct{})
	go watchSize(fd, session, width, height, done)
	err = session.Wait()
	close(done)
	if err == nil || errors.Is(err, io.EOF) {
		return nil
	}
	var exitErr *ssh.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitStatus() == 0 {
		return nil
	}
	return err
}

func dial(host config.SSHHost, password string, resolve HostResolver, resolvePassword PasswordResolver) (*ssh.Client, *ssh.Client, error) {
	cfg, err := clientConfig(host, password)
	if err != nil {
		return nil, nil, err
	}
	address := net.JoinHostPort(host.Hostname, strconv.Itoa(host.Port))
	if host.ProxyJump == "" {
		client, err := ssh.Dial("tcp", address, cfg)
		return client, nil, err
	}

	jumpHost, ok := resolve(host.ProxyJump)
	if !ok {
		return nil, nil, fmt.Errorf("ProxyJump host %q was not found", host.ProxyJump)
	}
	jumpPassword, err := resolvePassword(jumpHost.Name)
	if err != nil {
		return nil, nil, err
	}
	jumpCfg, err := clientConfig(*jumpHost, jumpPassword)
	if err != nil {
		return nil, nil, fmt.Errorf("ProxyJump %s: %w", jumpHost.Name, err)
	}
	jumpAddress := net.JoinHostPort(jumpHost.Hostname, strconv.Itoa(jumpHost.Port))
	jump, err := ssh.Dial("tcp", jumpAddress, jumpCfg)
	if err != nil {
		return nil, nil, fmt.Errorf("ProxyJump %s: %w", jumpHost.Name, err)
	}
	connection, err := jump.Dial("tcp", address)
	if err != nil {
		jump.Close()
		return nil, nil, err
	}
	conn, chans, reqs, err := ssh.NewClientConn(connection, address, cfg)
	if err != nil {
		connection.Close()
		jump.Close()
		return nil, nil, err
	}
	return ssh.NewClient(conn, chans, reqs), jump, nil
}

func clientConfig(host config.SSHHost, password string) (*ssh.ClientConfig, error) {
	auth, err := authMethods(host, password)
	if err != nil {
		return nil, err
	}
	if len(auth) == 0 {
		return nil, errors.New("no usable authentication method configured")
	}
	callback, err := hostKeyCallback()
	if err != nil {
		return nil, err
	}
	return &ssh.ClientConfig{User: host.User, Auth: auth, HostKeyCallback: callback, Timeout: 12 * time.Second}, nil
}

func authMethods(host config.SSHHost, password string) ([]ssh.AuthMethod, error) {
	var methods []ssh.AuthMethod
	if host.AuthType != config.AuthPrivateKey && password != "" {
		methods = append(methods, ssh.Password(password))
	}
	if host.AuthType != config.AuthPassword && host.IdentityFile != "" {
		filename, err := expandHome(host.IdentityFile)
		if err != nil {
			return nil, err
		}
		data, err := os.ReadFile(filename)
		if err != nil {
			return nil, fmt.Errorf("read private key: %w", err)
		}
		signer, err := ssh.ParsePrivateKey(data)
		if err != nil {
			return nil, fmt.Errorf("parse private key: %w", err)
		}
		methods = append(methods, ssh.PublicKeys(signer))
	}
	return methods, nil
}

func expandHome(filename string) (string, error) {
	if filename == "~" || strings.HasPrefix(filename, "~/") || strings.HasPrefix(filename, `~\`) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		if filename == "~" {
			return home, nil
		}
		return filepath.Join(home, filename[2:]), nil
	}
	return filename, nil
}

func hostKeyCallback() (ssh.HostKeyCallback, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	sshDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(sshDir, "known_hosts")
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			return nil, err
		}
	}
	known, err := knownhosts.New(path)
	if err != nil {
		return nil, err
	}
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		err := known(hostname, remote, key)
		if err == nil {
			return nil
		}
		var keyErr *knownhosts.KeyError
		if !errors.As(err, &keyErr) {
			return fmt.Errorf("host key verification failed: %w", err)
		}
		if expected, ok := fingerprintForType(keyErr.Want, key.Type()); ok {
			return fmt.Errorf(
				"host key verification failed for %s: %s key changed (expected %s, received %s)",
				hostname,
				key.Type(),
				expected,
				ssh.FingerprintSHA256(key),
			)
		}
		if len(keyErr.Want) == 0 {
			fmt.Printf("\nThe authenticity of host %s cannot be established.\n", hostname)
		} else {
			fmt.Printf("\nHost %s is known, but its %s key has not been saved yet.\n", hostname, key.Type())
		}
		fmt.Printf("Fingerprint: %s\nTrust this host and add it to %s? [y/N]: ", ssh.FingerprintSHA256(key), path)
		answer, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		if strings.ToLower(strings.TrimSpace(answer)) != "y" {
			return errors.New("host key was not trusted")
		}
		line := knownhosts.Line([]string{knownhosts.Normalize(hostname)}, key) + "\n"
		file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		defer file.Close()
		if _, err := file.WriteString(line); err != nil {
			return err
		}
		return nil
	}, nil
}

func fingerprintForType(keys []knownhosts.KnownKey, keyType string) (string, bool) {
	for _, known := range keys {
		if known.Key.Type() == keyType {
			return ssh.FingerprintSHA256(known.Key), true
		}
	}
	return "", false
}

func watchSize(fd int, session *ssh.Session, width, height int, done <-chan struct{}) {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			w, h, err := term.GetSize(fd)
			if err == nil && (w != width || h != height) {
				width, height = w, h
				_ = session.WindowChange(height, width)
			}
		}
	}
}
