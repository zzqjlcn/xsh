package credential

import (
	"errors"
	"fmt"
	"strings"

	"github.com/zalando/go-keyring"
)

const serviceName = "xsh"

var ErrUnavailable = errors.New("system credential store unavailable")

func SetPassword(alias, password string) error {
	return normalizeError(keyring.Set(serviceName, alias, password))
}

func GetPassword(alias string, prompt PasswordPrompt) (string, error) {
	if VaultExists() {
		password, found, err := getVaultPassword(alias, prompt)
		if err == nil && found {
			return password, nil
		}
		if err != nil {
			return "", err
		}
	}
	password, err := keyring.Get(serviceName, alias)
	keyringNotFound := errors.Is(err, keyring.ErrNotFound)
	keyringError := normalizeError(err)
	if err == nil && password != "" {
		// Migrate legacy OS credentials when a vault is available and unlocked.
		if VaultExists() {
			if err := SetVaultPassword(alias, password, prompt); err != nil {
				return "", err
			}
		}
		return password, nil
	}
	if keyringNotFound {
		return "", nil
	}
	return "", keyringError
}

func DeletePassword(alias string, prompt PasswordPrompt) error {
	keyringErr := keyring.Delete(serviceName, alias)
	if errors.Is(keyringErr, keyring.ErrNotFound) {
		keyringErr = nil
	} else {
		keyringErr = normalizeError(keyringErr)
	}
	removedFromVault, vaultErr := deleteVaultPassword(alias, prompt)
	if vaultErr != nil {
		return vaultErr
	}
	if removedFromVault && errors.Is(keyringErr, ErrUnavailable) {
		return nil
	}
	return keyringErr
}

func normalizeError(err error) error {
	if err == nil {
		return nil
	}
	message := strings.ToLower(err.Error())
	for _, marker := range []string{
		"org.freedesktop.secrets",
		"dbus_session_bus_address",
		"dbus-launch",
		"cannot autolaunch d-bus",
	} {
		if strings.Contains(message, marker) {
			return fmt.Errorf("%w: %v", ErrUnavailable, err)
		}
	}
	return err
}
