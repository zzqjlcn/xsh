package webui

import (
	"context"
	"errors"
	"io"
	"path"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// A separate SSH channel shares the connection's sshd parent with its terminal.
// Inspect that parent's PTY child without sending commands to the interactive
// terminal or changing the user's shell configuration. /proc is Linux-specific.
const workingDirectoryCommand = `exec sh -c '
IFS= read -r self < /proc/$$/stat || exit 1
set -- ${self##*) }
parent=$2
for entry in /proc/[0-9]*/stat; do
    IFS= read -r info < "$entry" 2>/dev/null || continue
    set -- ${info##*) }
    [ "$2" = "$parent" ] || continue
    [ "$5" != "0" ] || continue
    directory=${entry%/stat}
    cd "$directory/cwd" 2>/dev/null || continue
    pwd -P
    exit
done
exit 1
'`

func terminalWorkingDirectory(ctx context.Context, client *ssh.Client) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	session, err := client.NewSession()
	if err != nil {
		return "", err
	}
	defer session.Close()
	stop := context.AfterFunc(ctx, func() { _ = session.Close() })
	defer stop()
	stdout, err := session.StdoutPipe()
	if err != nil {
		return "", err
	}
	if err := session.Start(workingDirectoryCommand); err != nil {
		return "", err
	}
	data, err := io.ReadAll(io.LimitReader(stdout, 8193))
	if err != nil {
		return "", err
	}
	if len(data) > 8192 {
		return "", errors.New("terminal working directory is too long")
	}
	if err := session.Wait(); err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	directory := strings.TrimSuffix(string(data), "\n")
	if !path.IsAbs(directory) || strings.ContainsRune(directory, 0) {
		return "", errors.New("terminal working directory unavailable")
	}
	return directory, nil
}
