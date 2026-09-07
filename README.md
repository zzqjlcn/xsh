# xsh

`xsh` is a small Xshell-style terminal SSH manager inspired. It keeps connection metadata in a JSON file and stores saved passwords in the operating system credential store or an encrypted local vault.

## Features

- Host table with vim/arrow navigation, search, tags, and recent-login sorting
- Add, edit, and delete host profiles
- Passwords stored in Windows Credential Manager (Keychain/Secret Service on other platforms)
- Argon2id + AES-256-GCM encrypted vault fallback for headless Linux
- Password and private-key authentication
- One-hop `ProxyJump` through another saved host alias
- `known_hosts` verification with an explicit trust prompt for new hosts
- Real SSH PTY with terminal resize support
- Interactive local/SFTP file copying between the current machine and saved hosts
- Non-TUI `xsh cp` command with shell completion for saved host aliases
- Embedded local Web UI for searching, adding, editing, and deleting host profiles

## Run

```powershell
go run ./cmd/xsh
```

Or build a standalone executable:

```powershell
go build -o xsh.exe ./cmd/xsh
```

Profiles are stored in `%AppData%\xsh\hosts.json` on Windows. Passwords are never written to this file.
Set `XSH_CONFIG_HOME` to override the profile directory.

On Linux, `xsh` first uses a Secret Service provider such as GNOME Keyring or KWallet. If it is unavailable, `xsh` asks before creating `~/.config/xsh/secrets.db`. The file is encrypted with a master password that is requested when the vault is first opened in each process. The master password is never written to disk, and passwords never fall back to a plaintext file.

## Web UI

```shell
xsh web
xsh web --port 9090
```

Open the printed address (default `http://127.0.0.1:8080`) in your browser. Press Ctrl+C to stop. `--port 0` selects a free port. The UI is embedded in the executable, needs no separate frontend installation, and listens only on the local machine. Sign in with the encrypted password-vault master password; this unlocks saved credentials for the lifetime of the authenticated Web session. On first use, when no encrypted password vault exists, the login page asks you to create and confirm its master password before entering the terminal workspace.

The terminal workspace is the Web UI's main screen. The host list opens in a right-side drawer, while adding and editing hosts uses dialogs. Multiple terminal tabs can stay connected at the same time, and closing a tab disconnects only that SSH session. Existing aliases are read-only when editing to preserve saved credentials. A host password entered in the connection dialog is used for that connection only and is not saved. After Web login, encrypted-vault credentials are reused without requesting the master password again.

The embedded terminal renders the standard 16-color palette, ANSI 256-color sequences, bold bright colors, and 24-bit truecolor sequences. Remote applications receive an `xterm-256color` PTY; xsh also requests `COLORTERM=truecolor` when the SSH server permits that environment variable.

Browser terminals verify `~/.ssh/known_hosts`. A new host shows its key type and fingerprint for confirmation before saving it. A changed key is rejected. The Web UI and its terminal assets are embedded in the executable and the WebSocket endpoint accepts only same-origin requests with the per-process token.

## Keys

| Key | Action |
| --- | --- |
| `↑` / `↓`, `j` / `k` | Move selection |
| `Enter` | Connect |
| `a` | Add host |
| `e` | Edit host |
| `d` | Delete host |
| `cp` | Open the interactive file-copy wizard |
| `/` | Search |
| `s` | Sort by name/recent login |
| `q` | Quit |

## Copy files

Type `cp` on the host-list screen, then select the source and destination. Either endpoint can be `当前主机 (local)` or any saved SSH host. Press `Tab` while entering a local or remote path to complete matching files and directories; repeated presses cycle through multiple matches. File transfers use SFTP for remote endpoints, reuse the saved authentication settings, show byte/file progress, and can be cancelled with `Esc` or `q`.

Directories are copied recursively. Existing destination files may be replaced after the confirmation screen. Symbolic links and other special files are rejected instead of being followed.

The interactive copy screen shows the 10 most recent successful transfers. History is stored in `copy-history.json` beside `hosts.json` and contains paths only, never passwords.

### Command mode

Use a plain path for the current machine and `alias:/path` for a saved SSH host:

```shell
xsh cp ./release.tar.gz dev:/tmp/release.tar.gz
xsh cp dev:/var/log/app.log ./app.log
xsh cp dev:/tmp/data backup:/srv/data
```

`xsh cp` command mode does not open the full-screen TUI. Authentication and new-host-key confirmation may still prompt when required. Prefix a local path with `local:` if it could otherwise look like a remote location.

### Saved-host Tab completion

Load the completion script in the current shell, then type a saved-host prefix such as `xsh cp de` and press `Tab`:

```bash
# Bash
source <(xsh completion bash)

# Zsh
source <(xsh completion zsh)
```

```powershell
# PowerShell
xsh completion powershell | Out-String | Invoke-Expression
```

Add the matching command to the shell profile to enable completion in future sessions.
