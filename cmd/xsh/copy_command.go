package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/pkg/sftp"

	"github.com/zzqjlcn/xsh/internal/config"
	"github.com/zzqjlcn/xsh/internal/copyhistory"
	"github.com/zzqjlcn/xsh/internal/copyui"
	"github.com/zzqjlcn/xsh/internal/filecopy"
	"github.com/zzqjlcn/xsh/internal/sshclient"
)

type copyRuntime struct {
	store            *config.Store
	resolver         sshclient.HostResolver
	passwordResolver sshclient.PasswordResolver
	sessions         map[string]*remoteCopySession
}

type remoteCopySession struct {
	connection *sshclient.Connection
	sftp       *sftp.Client
}

func newCopyRuntime(store *config.Store) *copyRuntime {
	resolver, passwordResolver := sshResolvers(store)
	return &copyRuntime{
		store:            store,
		resolver:         resolver,
		passwordResolver: passwordResolver,
		sessions:         make(map[string]*remoteCopySession),
	}
}

func (r *copyRuntime) Close() {
	for _, session := range r.sessions {
		_ = session.sftp.Close()
		_ = session.connection.Close()
	}
}

func (r *copyRuntime) prepare(endpoint copyui.Endpoint) (*filecopy.FileSystem, error) {
	if endpoint.IsLocal() {
		return filecopy.LocalFileSystem(), nil
	}
	if session := r.sessions[endpoint.HostName]; session != nil {
		return filecopy.RemoteFileSystem(endpoint.HostName, session.sftp), nil
	}
	host := r.store.Find(endpoint.HostName)
	if host == nil {
		return nil, fmt.Errorf("host %q not found", endpoint.HostName)
	}
	password, err := passwordForHost(*host, fmt.Sprintf("Password for %s (not saved): ", host.Name))
	if err != nil {
		return nil, err
	}
	connection, err := sshclient.Dial(*host, password, r.resolver, r.passwordResolver)
	if err != nil {
		return nil, fmt.Errorf("connect %s: %w", host.Name, err)
	}
	sftpClient, err := sftp.NewClient(connection.Client)
	if err != nil {
		_ = connection.Close()
		return nil, fmt.Errorf("start SFTP on %s: %w", host.Name, err)
	}
	r.sessions[endpoint.HostName] = &remoteCopySession{connection: connection, sftp: sftpClient}
	return filecopy.RemoteFileSystem(endpoint.HostName, sftpClient), nil
}

func (r *copyRuntime) copy(ctx context.Context, request copyui.Request, report filecopy.Reporter) error {
	source, err := r.prepare(request.Source)
	if err != nil {
		return err
	}
	destination, err := r.prepare(request.Destination)
	if err != nil {
		return err
	}
	copier := filecopy.Copier{Source: source, Destination: destination}
	return copier.Copy(ctx, request.SourcePath, request.DestinationPath, report)
}

func runCopyCommand(store *config.Store, arguments []string, output io.Writer) error {
	if len(arguments) != 2 {
		return errors.New("usage: xsh cp <source> <destination> (remote: alias:/path)")
	}
	source, sourcePath, err := parseCopyLocation(store, arguments[0])
	if err != nil {
		return fmt.Errorf("source: %w", err)
	}
	destination, destinationPath, err := parseCopyLocation(store, arguments[1])
	if err != nil {
		return fmt.Errorf("destination: %w", err)
	}
	request := copyui.Request{
		Source:          source,
		SourcePath:      sourcePath,
		Destination:     destination,
		DestinationPath: destinationPath,
	}
	runtime := newCopyRuntime(store)
	defer runtime.Close()
	if err := runtime.copy(context.Background(), request, newCLIReporter(output)); err != nil {
		return err
	}
	if err := recordCopy(request); err != nil {
		return fmt.Errorf("copy completed, but history could not be saved: %w", err)
	}
	return nil
}

func parseCopyLocation(store *config.Store, value string) (copyui.Endpoint, string, error) {
	if value == "" {
		return copyui.Endpoint{}, "", errors.New("path is required")
	}
	if strings.HasPrefix(strings.ToLower(value), "local:") {
		path := value[len("local:"):]
		if path == "" {
			return copyui.Endpoint{}, "", errors.New("local path is required")
		}
		return copyui.Endpoint{}, path, nil
	}
	colon := strings.IndexByte(value, ':')
	if colon <= 0 || isWindowsDrivePath(value, colon) {
		return copyui.Endpoint{}, value, nil
	}
	alias, path := value[:colon], value[colon+1:]
	if host := store.Find(alias); host != nil {
		if path == "" {
			return copyui.Endpoint{}, "", fmt.Errorf("remote path for %q is required", host.Name)
		}
		return copyui.Endpoint{HostName: host.Name}, path, nil
	}
	if !strings.ContainsAny(alias, `/\`) && (strings.HasPrefix(path, "/") || strings.HasPrefix(path, "~/")) {
		return copyui.Endpoint{}, "", fmt.Errorf("saved host %q not found", alias)
	}
	return copyui.Endpoint{}, value, nil
}

func isWindowsDrivePath(value string, colon int) bool {
	return colon == 1 && len(value) > 2 && unicode.IsLetter(rune(value[0])) && (value[2] == '\\' || value[2] == '/')
}

func recordCopy(request copyui.Request) error {
	return copyhistory.Record(copyhistory.Entry{
		Source:      formatCopyLocation(request.Source, request.SourcePath),
		Destination: formatCopyLocation(request.Destination, request.DestinationPath),
	})
}

func formatCopyLocation(endpoint copyui.Endpoint, path string) string {
	if endpoint.IsLocal() {
		return "local:" + path
	}
	return endpoint.HostName + ":" + path
}

func newCLIReporter(output io.Writer) filecopy.Reporter {
	lastUpdate := time.Time{}
	return func(progress filecopy.Progress) {
		now := time.Now()
		if progress.Phase != "Complete" && !lastUpdate.IsZero() && now.Sub(lastUpdate) < 100*time.Millisecond {
			return
		}
		lastUpdate = now
		if progress.Phase == "Complete" {
			fmt.Fprintf(output, "\rCopy complete: %d file(s), %s          \n", progress.FilesCopied, humanBytes(progress.BytesCopied))
			return
		}
		fmt.Fprintf(output, "\r%s: %d/%d file(s), %s/%s", progress.Phase, progress.FilesCopied, progress.TotalFiles, humanBytes(progress.BytesCopied), humanBytes(progress.TotalBytes))
	}
}

func humanBytes(value int64) string {
	const unit = 1024
	if value < unit {
		return fmt.Sprintf("%d B", value)
	}
	divisor, exponent := int64(unit), 0
	for size := value / unit; size >= unit && exponent < 5; size /= unit {
		divisor *= unit
		exponent++
	}
	return fmt.Sprintf("%.1f %ciB", float64(value)/float64(divisor), "KMGTPE"[exponent])
}

func completeHosts(store *config.Store, prefix string) []string {
	if strings.Contains(prefix, ":") {
		return nil
	}
	prefix = strings.ToLower(prefix)
	result := make([]string, 0, len(store.Hosts))
	for _, host := range store.Hosts {
		if strings.HasPrefix(strings.ToLower(host.Name), prefix) {
			result = append(result, host.Name+":")
		}
	}
	sort.Slice(result, func(i, j int) bool { return strings.ToLower(result[i]) < strings.ToLower(result[j]) })
	return result
}

func completionScript(shell string) (string, error) {
	switch strings.ToLower(shell) {
	case "bash":
		return `_xsh_completion() {
  if [[ "${COMP_WORDS[1]}" == "cp" && ${COMP_CWORD} -ge 2 ]]; then
    local current="${COMP_WORDS[COMP_CWORD]}"
    compopt -o nospace
    mapfile -t COMPREPLY < <(xsh __complete-hosts "$current")
  fi
}
complete -F _xsh_completion xsh
`, nil
	case "zsh":
		return `#compdef xsh
_xsh() {
  if [[ "$words[2]" == "cp" && $CURRENT -ge 3 ]]; then
    local -a hosts
    hosts=("${(@f)$(xsh __complete-hosts "$PREFIX")}")
    compadd -S '' -a hosts
  fi
}
compdef _xsh xsh
`, nil
	case "powershell", "pwsh":
		return `Register-ArgumentCompleter -Native -CommandName xsh,xsh.exe -ScriptBlock {
    param($wordToComplete, $commandAst, $cursorPosition)
    $elements = @($commandAst.CommandElements | ForEach-Object { $_.Extent.Text })
    if ($elements.Count -ge 2 -and $elements[1] -eq 'cp') {
        xsh __complete-hosts $wordToComplete | ForEach-Object {
            [System.Management.Automation.CompletionResult]::new($_, $_, 'ParameterValue', $_)
        }
    }
}
`, nil
	default:
		return "", fmt.Errorf("unsupported shell %q (use bash, zsh, or powershell)", shell)
	}
}
