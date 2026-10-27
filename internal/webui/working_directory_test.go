package webui

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestTerminalWorkingDirectory(t *testing.T) {
	var cwd atomic.Value
	cwd.Store("/srv/项目 with spaces")
	address, stop := startTestSSHServerWithWorkingDirectory(t, "tester", func() string { return cwd.Load().(string) })
	defer stop()
	client, err := ssh.Dial("tcp", address, &ssh.ClientConfig{User: "tester", Auth: []ssh.AuthMethod{ssh.Password("secret")}, HostKeyCallback: ssh.InsecureIgnoreHostKey()})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	for _, directory := range []string{"/srv/项目 with spaces", "/tmp/other", "/"} {
		cwd.Store(directory)
		got, err := terminalWorkingDirectory(context.Background(), client)
		if err != nil || got != directory {
			t.Fatalf("cwd=%q: got %q, %v", directory, got, err)
		}
	}
	for _, invalid := range []string{"", "relative/path", "/path\x00invalid", "/" + strings.Repeat("x", 8192)} {
		cwd.Store(invalid)
		if directory, err := terminalWorkingDirectory(context.Background(), client); err == nil {
			t.Fatalf("accepted invalid directory %q", directory)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := terminalWorkingDirectory(ctx, client); err == nil {
		t.Fatal("ignored request cancellation")
	}
}
