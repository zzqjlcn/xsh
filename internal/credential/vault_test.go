package credential

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func TestVaultRoundTrip(t *testing.T) {
	t.Setenv("XSH_CONFIG_HOME", t.TempDir())
	resetVaultForTest()
	t.Cleanup(resetVaultForTest)

	responses := []string{"correct horse battery staple", "correct horse battery staple"}
	if err := SetVaultPassword("dev", "server-password", promptFrom(&responses)); err != nil {
		t.Fatal(err)
	}
	path, err := vaultPath()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "server-password") || strings.Contains(string(data), "dev") {
		t.Fatal("encrypted password file contains plaintext data")
	}

	resetVaultForTest()
	responses = []string{"correct horse battery staple"}
	password, found, err := getVaultPassword("dev", promptFrom(&responses))
	if err != nil {
		t.Fatal(err)
	}
	if !found || password != "server-password" {
		t.Fatalf("getVaultPassword() = %q, %v", password, found)
	}
	if len(responses) != 0 {
		t.Fatal("master password was not requested exactly once")
	}

	password, found, err = getVaultPassword("dev", func(string) (string, error) {
		t.Fatal("unlocked vault prompted for the master password again")
		return "", nil
	})
	if err != nil || !found || password != "server-password" {
		t.Fatalf("cached getVaultPassword() = %q, %v, %v", password, found, err)
	}
}

func TestVaultRejectsWrongMasterPassword(t *testing.T) {
	t.Setenv("XSH_CONFIG_HOME", t.TempDir())
	resetVaultForTest()
	t.Cleanup(resetVaultForTest)

	responses := []string{"correct horse battery staple", "correct horse battery staple"}
	if err := SetVaultPassword("dev", "server-password", promptFrom(&responses)); err != nil {
		t.Fatal(err)
	}
	resetVaultForTest()
	responses = []string{"wrong password", "wrong password", "wrong password"}
	_, _, err := getVaultPassword("dev", promptFrom(&responses))
	if !errors.Is(err, ErrInvalidMasterPassword) {
		t.Fatalf("getVaultPassword() error = %v", err)
	}
}

func TestVaultValidatesNewMasterPassword(t *testing.T) {
	t.Setenv("XSH_CONFIG_HOME", t.TempDir())
	resetVaultForTest()
	t.Cleanup(resetVaultForTest)

	responses := []string{"short", "short", "short"}
	if err := SetVaultPassword("dev", "password", promptFrom(&responses)); !errors.Is(err, ErrMasterPasswordTooShort) {
		t.Fatalf("short master password error = %v", err)
	}

	resetVaultForTest()
	responses = []string{
		"long enough password", "different password",
		"long enough password", "different password",
		"long enough password", "different password",
	}
	if err := SetVaultPassword("dev", "password", promptFrom(&responses)); !errors.Is(err, ErrMasterPasswordMismatch) {
		t.Fatalf("mismatched master password error = %v", err)
	}
}

func TestVaultRetriesInvalidNewMasterPassword(t *testing.T) {
	t.Setenv("XSH_CONFIG_HOME", t.TempDir())
	resetVaultForTest()
	t.Cleanup(resetVaultForTest)

	responses := []string{"short", "correct horse battery staple", "correct horse battery staple"}
	if err := SetVaultPassword("dev", "password", promptFrom(&responses)); err != nil {
		t.Fatal(err)
	}
	if len(responses) != 0 {
		t.Fatalf("unused prompt responses: %d", len(responses))
	}
}

func promptFrom(responses *[]string) PasswordPrompt {
	return func(string) (string, error) {
		if len(*responses) == 0 {
			return "", errors.New("unexpected password prompt")
		}
		response := (*responses)[0]
		*responses = (*responses)[1:]
		return response, nil
	}
}

func resetVaultForTest() {
	currentVault.Lock()
	defer currentVault.Unlock()
	clearBytes(currentVault.key)
	currentVault.path = ""
	currentVault.salt = nil
	currentVault.key = nil
	currentVault.passwords = nil
}
