# xsh

An SSH manager with a terminal interface and an embedded browser workspace. Organize hosts, work across terminal tabs and split panes, and transfer files from one executable.

**English** · [简体中文](README.zh-CN.md)

## Features

- **Host management** — searchable profiles, tags, recent connections, password/private-key authentication, and one-hop jump hosts.
- **Browser workspace** — terminal tabs, nested vertical/horizontal splits, context menus, and a close button on each split pane.
- **Independent accounts** — private host lists and encrypted credentials, administrator-managed users, and configuration-only sharing to multiple recipients.
- **File transfers** — SFTP browsing, uploads, and downloads in the browser; recursive local/remote copying from the CLI.
- **Password tools** — test SSH password authentication, change a remote Linux user's password, and generate/copy random passwords.
- **Local storage** — SQLite for profiles, history, accounts, and encrypted credentials. No separate database service or frontend installation.

## Quick start

Build from the repository with **Go 1.23.4 or later**:

```sh
go build -o xsh ./cmd/xsh
./xsh web
```

On Windows:

```powershell
go build -o xsh.exe ./cmd/xsh
.\xsh.exe web
```

Open the printed address, normally `http://127.0.0.1:8080/`. On first run:

1. Copy the administrator setup code printed in the terminal.
2. Enter it in the browser and create the first account, which becomes the administrator.
3. Add a host and connect. Create other accounts under **Settings → User management** (`设置 → 用户管理`).

There is no public account registration. The setup code is also stored in `web-setup-code` inside the configuration directory.

Run `./xsh` without a subcommand to open the terminal interface. For development, `go run ./cmd/xsh` and `go run ./cmd/xsh web` work without a separate build. Examples below assume `xsh` is on your `PATH`.

## Docker

With Docker Compose:

```sh
docker compose up -d --build
docker compose logs -f xsh
```

Open `http://localhost:8080/` and use the setup code from the logs. Compose saves data in its `xsh-data` named volume and restarts the service automatically unless explicitly stopped. `docker compose down` preserves data; adding `-v` deletes the data volume.

Or build and run directly:

```sh
docker build -t xsh .
docker run -d --name xsh -p 8080:8080 -v xsh-data:/data xsh
docker logs xsh
```

Open `http://localhost:8080/` and use the administrator setup code from the logs. The multi-stage build produces a static binary and uses `scratch` as the runtime image, running as user `65532:65532`. The `/data` volume preserves the database, encrypted credentials, setup code, and SSH host keys. Host bind mounts must be writable by this user. To set the version shown in About xsh, build with `--build-arg VERSION=v1.0.0`.

Override the default arguments as needed, for example `docker run -d --name xsh -p 8080:8080 -v xsh-data:/data xsh web --host 0.0.0.0 --port 8080 --root-path /xsh`.

## Browser workspace

### Hosts and connections

Open the host-list icon in the toolbar to add, search, edit, or connect to a host. Each account maintains its own list; different accounts can reuse the same alias. Existing aliases are read-only in the editor.

Click a host with a saved password to connect immediately as its configured SSH user; hosts without a saved password still open the connection dialog. The same applies to keyboard selection and recent hosts. Right-click a host for **Connect, Log in as another user, Edit, Share,** or **Delete**. Logging in as another user opens a form for a username and password or private key; deletion requires confirmation.

- **Authentication:** choose password or private key. A new password profile can be saved with an empty password to prompt at connection time; a blank edit preserves an existing saved password. Private keys may be pasted or selected from a local file (up to 64 KB, without a passphrase). The browser reads the file contents; it never sends a local file path for the server to open.
- **Jump hosts:** select another host from your list. The searchable dropdown supports one-hop forwarding.
- **Test a password:** use the plug/check icon inside the host-password field. It tests the current address, port, username, and selected jump host without saving the form or opening a terminal. When editing, a blank password uses the saved password. The test uses password authentication and has a 15-second deadline.
- **Temporary SSH user:** the connection dialog defaults to the configured username. Change it to connect as another user with a password or private key for that connection. Both the connection and retry dialogs support pasting a private key or selecting a local key file. It does not reuse the default user's saved password/private key or change the profile.

New server keys require fingerprint confirmation; changed keys are rejected. Password tests trust a new fingerprint for that test only; normal terminal connections save confirmed keys.

### Tabs and split panes

Press **Alt+O** anywhere in the unlocked workspace, including inputs and terminals, to open the host list and focus its search field. Any open dialog closes first. Pressing it again keeps the list open.

**Settings → Security** enables automatic screen locking after a configurable period without keyboard, mouse, or touch activity (on by default; choose 1, 5, 10, 15, 30, or 60 minutes, default 5). Terminal output does not reset the timer. You can also enable immediate locking when switching browser tabs or minimizing the window (off by default), or lock immediately. These preferences are saved per account in the current browser. Locking keeps SSH connections open and requires the login password to resume. Background/sleep time counts toward inactivity; the page checks the deadline again when it becomes active. While locked, login validity is checked every 30 seconds and immediately when returning to the page. An expired session goes directly to login; network or server errors leave the screen locked.

Click **+** beside the tabs to open a home tab while existing connections stay open. Drag a tab left or right to reorder it without reconnecting its terminals; keyboard switching follows the displayed order. **Ctrl+Tab** switches to the next workspace tab and **Ctrl+Shift+Tab** to the previous one, wrapping at either end. Browsers may reserve these combinations for browser tabs; use **Alt+PageDown / Alt+PageUp** as alternatives. Right-click a tab for **New, Duplicate, Reconnect, Close, Close others, Close all, Vertical split,** and **Horizontal split**.

Authentication failures open the connection dialog for fresh credentials. Retrying reuses the same terminal pane and tab. **Reconnect** is available for the focused failed or disconnected terminal; it reuses saved credentials when appropriate, otherwise asks for a password or private key. Temporary retry credentials are not saved, and other split panes stay connected.

Vertical splits place terminals side by side; horizontal splits stack them. Splits can be nested. Duplication and splitting establish a new SSH connection for the current terminal's user; temporary users must provide their password or private key again. Right-click the relevant pane to split that specific terminal.

Each split pane has a close button in its upper-right corner. Closing it disconnects only that terminal and expands the remaining layout. Closing a tab disconnects all terminals inside it.

The terminal context menu provides **Copy, Paste, Select all, Clear, Split,** and **Close current terminal**. Clear affects the local terminal display, not the remote shell. File-transfer and remote-password toolbar actions follow the focused terminal. Browser clipboard access depends on the browser and deployment; use keyboard paste if clipboard reading is unavailable.

Terminals support ANSI 16/256 colors and truecolor, an `xterm-256color` PTY, and window resizing. xsh also requests `COLORTERM=truecolor` when the server allows it.

### Accounts, sharing, and screen lock

The settings icon contains **My account**, **Appearance**, **About xsh**, and, for administrators, **User management**. About xsh shows the running version, source revision and commit time (when available), platform, Go version, and project link. Local builds show `dev`; set a release version with `go build -ldflags "-X github.com/zzqjlcn/xsh/internal/webui.Version=v1.0.0" -o xsh ./cmd/xsh`. Administrators can create, enable, disable, and delete ordinary accounts. Deletion requires confirmation, removes their hosts, saved credentials and sent/received shares, and disconnects their sessions. Administrator accounts cannot be deleted. The login page remembers the last successfully used username in the current browser; it does not save the login password.

To share a host, open its editor and select **Share configuration**. Search and select one or more recipient accounts. The recipient opens the sharing inbox, chooses **Add**, and supplies their own SSH username and credentials.

| Shared | Never shared |
| --- | --- |
| Name, address, port, tags | SSH username, passwords, private keys/paths, authentication settings, jump-host references, saved-password flags, login history |

Accepted shares are independent copies. Sender edits or deletion do not affect the recipient. Rename duplicate aliases before accepting; failed imports leave the share available for retry. Sharing does not grant access to the sender's credentials or sessions.

**Lock** hides the workspace and requires the current account password to resume, keeping existing SSH connections, terminal buffers, and transfers. Terminal input and new API operations are blocked while locked; remote output continues. **Log out** closes that login session's connections. Refreshing or closing the page disconnects its terminals, including when the screen is locked.

Login sessions last up to 12 hours and expire when xsh restarts. Changing an account password or disabling an account revokes all of its sessions. Changing the login password preserves saved credentials. Administrator password reset/account-key recovery is not currently available.

### File transfers

After connecting, use the file-transfer toolbar icon to browse the focused terminal's host over its existing SSH connection. Each time it opens, it reads the terminal shell's current working directory on Linux. If the server cannot provide it, the dialog opens the user's home directory with a notice. Click anywhere in a folder row to open it, or anywhere in a file row to select it. You can also enter a path or use the up/refresh controls.

Upload multiple files with the picker or drag them onto the current directory or a folder row. Transfers show progress and support cancellation; existing remote filenames are rejected. Downloads use the browser download manager. Browser transfers do not recursively upload/download directories; use the CLI for recursive copying.

### Change a remote password

Use the key icon in the toolbar of a connected terminal to change the **currently authenticated remote user's** operating-system password. This does not change the xsh account password or CLI vault master password.

The current-password field can use the matching profile/user's saved password, or an explicitly entered value. Its eye/copy icons can reveal or copy a saved password on demand. Key-only users may need to enter their current system password; root may leave it blank if the server permits.

New passwords must be at least eight characters and meet the remote server's policy. The field includes show/hide, copy, and generation buttons. Generated passwords contain 20 characters with uppercase/lowercase letters, digits, and symbols; generation fills the confirmation field too.

The operation runs `LC_ALL=C passwd` in a separate SSH PTY with a 30-second deadline. Standard English Linux prompts are supported; custom PAM/OTP prompts, network devices, and Windows servers are not. Passwords are sent through SSH input, never command arguments.

On success, an already-saved password is updated only for the matching host and SSH user in the current account. Temporary credentials remain unsaved; other accounts and CLI credentials are unaffected. The active terminal stays connected. Copy the new password before closing the dialog. If a timeout or interruption leaves the result uncertain, verify the remote password before retrying; the dialog prevents immediate resubmission.

## Terminal interface and file-copy commands

Run `xsh` to open the terminal host list:

| Key | Action |
| --- | --- |
| `↑` / `↓`, `j` / `k` | Move selection |
| `Enter` | Connect |
| `a` / `e` / `d` | Add / edit / delete host |
| `/` | Search |
| `s` | Sort by name / recent login |
| `cp` | Open the file-copy wizard |
| `q` | Quit |

The wizard supports local ↔ remote and remote ↔ remote copying. Press `Tab` in a path field for completion; repeated presses cycle matches. Transfers show byte/file progress, can be cancelled with `Esc` or `q`, and retain the ten most recent successful transfers. Directories are copied recursively; destination files may be replaced after confirmation. Symbolic links and special files are rejected.

For command mode, use a plain path locally and `alias:/path` for a saved CLI host:

```sh
xsh cp ./release.tar.gz dev:/tmp/release.tar.gz
xsh cp dev:/var/log/app.log ./app.log
xsh cp dev:/tmp/data backup:/srv/data
```

Use `local:` when a local path could be mistaken for a remote endpoint. Command mode avoids the full-screen interface, but authentication and new-host-key verification may still prompt.

Enable saved-host completion in your shell; add the corresponding command to your shell profile to keep it enabled:

```bash
# Bash
source <(xsh completion bash)
# Zsh
source <(xsh completion zsh)
```

```powershell
xsh completion powershell | Out-String | Invoke-Expression
```

## Data and credentials

| Platform | Default database |
| --- | --- |
| Windows | `%AppData%\xsh\xsh.db` |
| Linux | `~/.config/xsh/xsh.db` (or `$XDG_CONFIG_HOME/xsh/xsh.db`) |
| macOS | `~/Library/Application Support/xsh/xsh.db` |

Set `XSH_CONFIG_HOME` to override the directory. CLI profiles/history/vault and browser accounts/hosts/shares use separate tables in the same SQLite database. The CLI vault uses Argon2id and AES-256-GCM with its master password. Browser accounts have separate random encryption keys wrapped with keys derived from their login passwords; SSH passwords and private keys are encrypted. Profile metadata is not encrypted.

Legacy `hosts.json`, `copy-history.json`, and encrypted `secrets.db` are imported automatically in one transaction on first initialization. Original files remain as backups. Existing system-keyring credentials can be read and copied into the unlocked CLI vault; new saves use SQLite, and vault entries take precedence.

During administrator setup, an existing CLI host list can optionally be copied into the first account. Importing encrypted credentials requires the CLI master password. The imported credentials are re-encrypted for that account; CLI data and its master-password workflow remain unchanged. Other accounts start empty.

Browser host-key records live in `web-known-hosts/<user-id>` in the configuration directory. The CLI uses `~/.ssh/known_hosts`.

Stop xsh before backing up the configuration directory. Keep the CLI master password and account passwords separately: a database backup alone cannot recover encrypted credentials without their passwords. Local access to the database and process memory is part of the deployment trust boundary.

## Network deployment

```sh
xsh web --port 9090
xsh web --port 0
xsh web --host 0.0.0.0 --port 8080 --root-path /xsh
```

| Option | Default | Meaning |
| --- | --- | --- |
| `--host` | `127.0.0.1` | Listen address; `0.0.0.0` accepts network connections |
| `--port` | `8080` | Listen port; `0` selects an available port |
| `--root-path` | `/` | Mount path, including nested paths such as `/tools/xsh` |

Use HTTPS through a trusted reverse proxy for network access. For the `/xsh` example, add this to an existing Nginx server block:

```nginx
location = /xsh {
    return 308 /xsh/$is_args$args;
}

location /xsh/ {
    proxy_pass http://127.0.0.1:8080;
    proxy_http_version 1.1;
    proxy_set_header Host $http_host;
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection "upgrade";
    proxy_read_timeout 3600s;
    proxy_buffering off;
}
```

Keep `proxy_pass` without a trailing slash to preserve the prefix, and preserve the browser's `Origin` header. Static files, APIs, WebSockets, and cookie paths follow `--root-path`. Restrict direct access to the upstream port if it should be reachable only through the proxy. Set Nginx's `client_max_body_size` for your upload needs.

Browser connections require the current login session and its token; terminal/transfer connections belong to their creating session. Account isolation does not restrict the remote user's operating-system permissions. Target-IP allowlists are not implemented.

## Development

```sh
go test ./...
go vet ./...
```

Frontend assets are in `internal/webui/static` and embedded at build time. Rebuild and restart xsh after changing them. For contributions, include reproduction steps or intended behavior, relevant checks, and screenshots for UI changes. Update both language versions when changing documented behavior.
