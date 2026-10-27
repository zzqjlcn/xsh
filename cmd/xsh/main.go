package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/zzqjlcn/xsh/internal/config"
	"github.com/zzqjlcn/xsh/internal/copyhistory"
	"github.com/zzqjlcn/xsh/internal/copyui"
	"github.com/zzqjlcn/xsh/internal/credential"
	"github.com/zzqjlcn/xsh/internal/filecopy"
	"github.com/zzqjlcn/xsh/internal/form"
	"github.com/zzqjlcn/xsh/internal/sshclient"
	"github.com/zzqjlcn/xsh/internal/ui"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "xsh:", err)
		os.Exit(1)
	}
}

func run(arguments []string) error {
	store, err := config.Load()
	if err != nil {
		return err
	}
	if len(arguments) > 0 {
		switch arguments[0] {
		case "web":
			return runWebCommand(arguments[1:], os.Stdout)
		case "cp":
			return runCopyCommand(store, arguments[1:], os.Stdout)
		case "completion":
			if len(arguments) != 2 {
				return errors.New("usage: xsh completion <bash|zsh|powershell>")
			}
			script, err := completionScript(arguments[1])
			if err != nil {
				return err
			}
			fmt.Fprint(os.Stdout, script)
			return nil
		case "__complete-hosts":
			prefix := ""
			if len(arguments) > 1 {
				prefix = arguments[1]
			}
			for _, candidate := range completeHosts(store, prefix) {
				fmt.Fprintln(os.Stdout, candidate)
			}
			return nil
		default:
			return fmt.Errorf("unknown command %q (available: cp, web, completion)", arguments[0])
		}
	}

	for {
		program := tea.NewProgram(ui.New(store.Hosts), tea.WithAltScreen())
		final, err := program.Run()
		if err != nil {
			return err
		}

		model, ok := final.(ui.Model)
		if !ok {
			return errors.New("unexpected UI state")
		}

		result := model.Result()
		switch result.Action {
		case ui.ActionQuit, ui.ActionNone:
			return nil
		case ui.ActionAdd:
			if err := addHost(store); err != nil {
				pauseOnError(err)
			}
		case ui.ActionEdit:
			if result.Host != nil {
				if err := editHost(store, *result.Host); err != nil {
					pauseOnError(err)
				}
			}
		case ui.ActionDelete:
			if result.Host != nil {
				if err := deleteHost(store, *result.Host); err != nil {
					pauseOnError(err)
				}
			}
		case ui.ActionConnect:
			if result.Host != nil {
				if err := connect(store, *result.Host); err != nil {
					pauseOnError(err)
				}
			}
		case ui.ActionCopy:
			if err := copyFiles(store); err != nil {
				pauseOnError(err)
			}
		}
	}
}

func addHost(store *config.Store) error {
	result, err := form.Edit(nil)
	if err != nil || !result.Save {
		return err
	}
	wantsSavedPassword := result.PasswordChanged && result.SavePassword
	result.Host.SavePassword = false
	if err := store.Add(result.Host); err != nil {
		return err
	}
	if wantsSavedPassword {
		if err := savePassword(result.Host.Name, result.Password); err != nil {
			if errors.Is(err, credential.ErrUnavailable) {
				return fmt.Errorf("host saved, but password was not saved; it will be requested when connecting: %w", err)
			}
			return fmt.Errorf("host saved, but password could not be saved: %w", err)
		}
		host := store.Find(result.Host.Name)
		host.SavePassword = true
		return store.Save()
	}
	return nil
}

func editHost(store *config.Store, current config.SSHHost) error {
	result, err := form.Edit(&current)
	if err != nil || !result.Save {
		return err
	}

	oldPassword := ""
	err = nil
	if current.SavePassword {
		oldPassword, err = credential.GetPassword(current.Name, form.ReadPassword)
	}
	credentialUnavailable := errors.Is(err, credential.ErrUnavailable)
	if err != nil && !credentialUnavailable {
		return fmt.Errorf("read saved password: %w", err)
	}

	warning := error(nil)
	if result.PasswordChanged {
		if result.SavePassword {
			oldPassword = result.Password
			result.Host.SavePassword = true
		} else {
			oldPassword = ""
			result.Host.SavePassword = false
		}
	} else if !credentialUnavailable {
		result.Host.SavePassword = oldPassword != ""
	}

	if result.Host.SavePassword && (result.PasswordChanged || current.Name != result.Host.Name) {
		if credentialUnavailable && !result.PasswordChanged {
			result.Host.SavePassword = false
			warning = fmt.Errorf("host saved, but password was not saved; it will be requested when connecting: %w", credential.ErrUnavailable)
		} else if err := savePassword(result.Host.Name, oldPassword); err != nil {
			if errors.Is(err, credential.ErrUnavailable) {
				result.Host.SavePassword = false
				warning = fmt.Errorf("host saved, but password was not saved; it will be requested when connecting: %w", err)
			} else {
				return fmt.Errorf("save password: %w", err)
			}
		}
	}

	if err := store.Update(current.Name, result.Host); err != nil {
		return err
	}
	if current.Name != result.Host.Name && !credentialUnavailable {
		_ = credential.DeletePassword(current.Name, form.ReadPassword)
	}
	if oldPassword == "" && !credentialUnavailable {
		if err := credential.DeletePassword(result.Host.Name, form.ReadPassword); err != nil {
			if errors.Is(err, credential.ErrUnavailable) {
				return fmt.Errorf("host saved, but the old password could not be removed: %w", err)
			}
			return err
		}
	}
	return warning
}

func deleteHost(store *config.Store, host config.SSHHost) error {
	reader := bufio.NewReader(os.Stdin)
	fmt.Printf("Delete %s (%s@%s)? [y/N]: ", host.Name, host.User, host.Hostname)
	answer, _ := reader.ReadString('\n')
	if strings.ToLower(strings.TrimSpace(answer)) != "y" {
		return nil
	}
	if err := store.Delete(host.Name); err != nil {
		return err
	}
	if !host.SavePassword {
		return nil
	}
	if err := credential.DeletePassword(host.Name, form.ReadPassword); err != nil {
		if errors.Is(err, credential.ErrUnavailable) {
			return fmt.Errorf("host deleted, but its saved password could not be removed: %w", err)
		}
		return err
	}
	return nil
}

func connect(store *config.Store, host config.SSHHost) error {
	password, err := passwordForHost(host, "Password (not saved): ")
	if err != nil {
		return err
	}
	resolver, passwordResolver := sshResolvers(store)

	if err := sshclient.Connect(host, password, resolver, passwordResolver); err != nil {
		return err
	}
	return store.RecordLogin(host.Name)
}

func copyFiles(store *config.Store) error {
	history, err := copyhistory.Load()
	if err != nil {
		return fmt.Errorf("load copy history: %w", err)
	}
	runtime := newCopyRuntime(store)
	defer runtime.Close()

	wizard := copyui.NewWizard(store.Hosts, history)
	var copyRequest *copyui.Request
	for {
		wizardProgram := tea.NewProgram(wizard, tea.WithAltScreen())
		final, err := wizardProgram.Run()
		if err != nil {
			return err
		}
		updatedWizard, ok := final.(copyui.Wizard)
		if !ok {
			return errors.New("unexpected copy wizard state")
		}
		wizard = updatedWizard
		result := wizard.Result()
		if result.Cancelled {
			return nil
		}
		if result.PrepareEndpoint != nil {
			files, err := runtime.prepare(*result.PrepareEndpoint)
			if err != nil {
				return err
			}
			wizard.SetFileSystem(*result.PrepareEndpoint, files)
			continue
		}
		copyRequest = result.Request
		break
	}
	if copyRequest == nil {
		return nil
	}

	progress := copyui.NewProgress(func(ctx context.Context, report filecopy.Reporter) error {
		return runtime.copy(ctx, *copyRequest, report)
	})
	progressProgram := tea.NewProgram(progress, tea.WithAltScreen())
	final, err := progressProgram.Run()
	if err != nil {
		return err
	}
	progressResult, ok := final.(copyui.ProgressModel)
	if !ok {
		return errors.New("unexpected copy progress state")
	}
	if progressResult.Result() != nil {
		return nil
	}
	if err := recordCopy(*copyRequest); err != nil {
		return fmt.Errorf("copy completed, but history could not be saved: %w", err)
	}
	return nil
}

func sshResolvers(store *config.Store) (sshclient.HostResolver, sshclient.PasswordResolver) {
	resolver := func(name string) (*config.SSHHost, bool) {
		found := store.Find(name)
		if found == nil {
			return nil, false
		}
		copy := *found
		return &copy, true
	}
	passwordResolver := func(name string) (string, error) {
		host, ok := resolver(name)
		if !ok {
			return "", fmt.Errorf("host %q not found", name)
		}
		return passwordForHost(*host, fmt.Sprintf("Password for jump host %s (not saved): ", name))
	}
	return resolver, passwordResolver
}

func passwordForHost(host config.SSHHost, prompt string) (string, error) {
	if host.AuthType == config.AuthPrivateKey {
		return "", nil
	}
	password := ""
	var err error
	if host.SavePassword {
		password, err = credential.GetPassword(host.Name, form.ReadPassword)
	}
	if err != nil && !errors.Is(err, credential.ErrUnavailable) {
		return "", fmt.Errorf("read saved password for %s: %w", host.Name, err)
	}
	if password != "" {
		return password, nil
	}
	return form.ReadPassword(prompt)
}

func savePassword(alias, password string) error {
	return credential.SetVaultPassword(alias, password, form.ReadPassword)
}

func pauseOnError(err error) {
	label := "Error:"
	if errors.Is(err, credential.ErrUnavailable) {
		label = "Warning:"
	}
	fmt.Fprintln(os.Stderr, "\n"+label, err)
	fmt.Fprint(os.Stderr, "Press Enter to return...")
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
}
