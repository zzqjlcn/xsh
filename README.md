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
