package credential

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"sync"

	"golang.org/x/crypto/argon2"

	"github.com/zzqjlcn/xsh/internal/database"
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
	_, err := vaultPath()
	if err != nil {
		return false
	}
	_, err = database.ReadVault()
	return err == nil
}

// UnlockVault verifies the existing vault master password and keeps the vault
// unlocked for subsequent credential reads in this process.
func UnlockVault(password string) error {
	if !VaultExists() {
		return errors.New("encrypted password vault not found; create it from the terminal UI first")
	}
	currentVault.Lock()
	defer currentVault.Unlock()
	path, err := vaultPath()
	if err != nil {
		return err
	}
	if currentVault.key != nil && currentVault.path == path {
		candidate := deriveKey(password, currentVault.salt)
		defer clearBytes(candidate)
		if subtle.ConstantTimeCompare(candidate, currentVault.key) != 1 {
			return ErrInvalidMasterPassword
		}
		return nil
	}
	return currentVault.unlock(func(string) (string, error) { return password, nil }, false)
}

// CreateVault creates and unlocks an empty encrypted password vault.
func CreateVault(password string) error {
	if len([]rune(password)) < 8 {
		return ErrMasterPasswordTooShort
	}
	currentVault.Lock()
	defer currentVault.Unlock()
	_, err := vaultPath()
	if err != nil {
		return err
	}
	if _, err := database.ReadVault(); err == nil {
		return errors.New("encrypted password vault already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := currentVault.unlock(func(string) (string, error) { return password, nil }, true); err != nil {
		return err
	}
	return currentVault.save()
}

// LockVault clears the decrypted password vault from process memory.
func LockVault() {
	currentVault.Lock()
	defer currentVault.Unlock()
	clearBytes(currentVault.key)
	currentVault.path = ""
	currentVault.salt = nil
	currentVault.key = nil
	currentVault.passwords = nil
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

	data, err := database.ReadVault()
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
	return database.WriteVault(data)
}

func vaultPath() (string, error) { return database.Path() }

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
