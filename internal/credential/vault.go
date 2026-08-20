package credential

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/crypto/argon2"

	"github.com/zzqjlcn/xsh/internal/config"
)

const (
	vaultVersion = 1
	vaultTime    = 3
	vaultMemory  = 64 * 1024
	vaultThreads = 4
	vaultKeySize = 32
)

var (
	ErrInvalidMasterPassword  = errors.New("invalid master password")
	ErrMasterPasswordMismatch = errors.New("master passwords do not match")
	ErrMasterPasswordTooShort = errors.New("master password must contain at least 8 characters")
)

type PasswordPrompt func(label string) (string, error)

type vaultFile struct {
	Version    int    `json:"version"`
	Salt       string `json:"salt"`
	Nonce      string `json:"nonce"`
	Ciphertext string `json:"ciphertext"`
}

type vaultPayload struct {
	Passwords map[string]string `json:"passwords"`
}

type vaultSession struct {
	sync.Mutex
	path      string
	salt      []byte
	key       []byte
	passwords map[string]string
}

var currentVault vaultSession

func VaultExists() bool {
	path, err := vaultPath()
	if err != nil {
		return false
	}
	_, err = os.Stat(path)
	return err == nil
}

func SetVaultPassword(alias, password string, prompt PasswordPrompt) error {
	currentVault.Lock()
	defer currentVault.Unlock()
	if err := currentVault.unlock(prompt, true); err != nil {
		return err
	}
	currentVault.passwords[alias] = password
	return currentVault.save()
}

func getVaultPassword(alias string, prompt PasswordPrompt) (string, bool, error) {
	currentVault.Lock()
	defer currentVault.Unlock()
	if err := currentVault.unlock(prompt, false); err != nil {
		return "", false, err
	}
	password, ok := currentVault.passwords[alias]
	return password, ok, nil
}

func deleteVaultPassword(alias string, prompt PasswordPrompt) (bool, error) {
	if !VaultExists() {
		return false, nil
	}
	currentVault.Lock()
	defer currentVault.Unlock()
	if err := currentVault.unlock(prompt, false); err != nil {
		return false, err
	}
	if _, ok := currentVault.passwords[alias]; !ok {
		return false, nil
	}
	delete(currentVault.passwords, alias)
	return true, currentVault.save()
}

func (v *vaultSession) unlock(prompt PasswordPrompt, create bool) error {
	path, err := vaultPath()
	if err != nil {
		return err
	}
	if v.key != nil && v.path == path {
		return nil
	}
	if v.key != nil {
		clearBytes(v.key)
		v.path = ""
		v.salt = nil
		v.key = nil
		v.passwords = nil
	}
	if prompt == nil {
		return errors.New("master password prompt is unavailable")
	}

	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		if !create {
			return os.ErrNotExist
		}
		return v.create(path, prompt)
	}
	if err != nil {
		return err
	}
	return v.open(path, data, prompt)
}

func (v *vaultSession) create(path string, prompt PasswordPrompt) error {
	label := "Create master password (at least 8 characters): "
	lastError := error(ErrMasterPasswordTooShort)
	masterPassword := ""
	for attempt := 0; attempt < 3; attempt++ {
		value, err := prompt(label)
		if err != nil {
			return err
		}
		if len([]rune(value)) < 8 {
			label = "Master password is too short; try again: "
			lastError = ErrMasterPasswordTooShort
			continue
		}
		confirmation, err := prompt("Confirm master password: ")
		if err != nil {
			return err
		}
		if value != confirmation {
			label = "Master passwords did not match; try again: "
			lastError = ErrMasterPasswordMismatch
			continue
		}
		masterPassword = value
		break
	}
	if masterPassword == "" {
		return lastError
	}

	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	key := deriveKey(masterPassword, salt)
	v.path = path
	v.salt = salt
	v.key = key
	v.passwords = make(map[string]string)
	return nil
}

func (v *vaultSession) open(path string, data []byte, prompt PasswordPrompt) error {
	var file vaultFile
	if err := json.Unmarshal(data, &file); err != nil {
		return fmt.Errorf("read encrypted password file: %w", err)
	}
	if file.Version != vaultVersion {
		return fmt.Errorf("unsupported password file version %d", file.Version)
	}
	salt, err := base64.StdEncoding.DecodeString(file.Salt)
	if err != nil {
		return fmt.Errorf("decode password file salt: %w", err)
	}
	nonce, err := base64.StdEncoding.DecodeString(file.Nonce)
	if err != nil {
		return fmt.Errorf("decode password file nonce: %w", err)
	}
	ciphertext, err := base64.StdEncoding.DecodeString(file.Ciphertext)
	if err != nil {
		return fmt.Errorf("decode encrypted passwords: %w", err)
	}

	for attempt := 0; attempt < 3; attempt++ {
		masterPassword, err := prompt("Master password: ")
		if err != nil {
			return err
		}
		key := deriveKey(masterPassword, salt)
		plaintext, err := decryptVault(key, nonce, ciphertext)
		if err != nil {
			clearBytes(key)
			continue
		}
		var payload vaultPayload
		if err := json.Unmarshal(plaintext, &payload); err != nil {
			clearBytes(plaintext)
			clearBytes(key)
			return fmt.Errorf("read decrypted passwords: %w", err)
		}
		clearBytes(plaintext)
		if payload.Passwords == nil {
			payload.Passwords = make(map[string]string)
		}
		v.path = path
		v.salt = salt
		v.key = key
		v.passwords = payload.Passwords
		return nil
	}
	return ErrInvalidMasterPassword
}

func (v *vaultSession) save() error {
	payload, err := json.Marshal(vaultPayload{Passwords: v.passwords})
	if err != nil {
		return err
	}
	defer clearBytes(payload)
	block, err := aes.NewCipher(v.key)
	if err != nil {
		return err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	ciphertext := gcm.Seal(nil, nonce, payload, []byte("xsh-secrets-v1"))
	file := vaultFile{
		Version:    vaultVersion,
		Salt:       base64.StdEncoding.EncodeToString(v.salt),
		Nonce:      base64.StdEncoding.EncodeToString(nonce),
		Ciphertext: base64.StdEncoding.EncodeToString(ciphertext),
	}
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}
	temporary := v.path + ".tmp"
	if err := os.WriteFile(temporary, data, 0o600); err != nil {
		return err
	}
	if err := os.Chmod(temporary, 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, v.path)
}

func vaultPath() (string, error) {
	dir, err := config.Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "secrets.db"), nil
}

func deriveKey(password string, salt []byte) []byte {
	passwordBytes := []byte(password)
	defer clearBytes(passwordBytes)
	return argon2.IDKey(passwordBytes, salt, vaultTime, vaultMemory, vaultThreads, vaultKeySize)
}

func decryptVault(key, nonce, ciphertext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(nonce) != gcm.NonceSize() {
		return nil, errors.New("invalid encrypted password nonce")
	}
	return gcm.Open(nil, nonce, ciphertext, []byte("xsh-secrets-v1"))
}

func clearBytes(value []byte) {
	for i := range value {
		value[i] = 0
	}
}
