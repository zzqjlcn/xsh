package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type AuthType string

const (
	AuthAuto       AuthType = "auto"
	AuthPassword   AuthType = "password"
	AuthPrivateKey AuthType = "private_key"
)

type SSHHost struct {
	Name         string   `json:"name"`
	Hostname     string   `json:"hostname"`
	User         string   `json:"user"`
	Port         int      `json:"port"`
	Tags         []string `json:"tags,omitempty"`
	AuthType     AuthType `json:"auth_type"`
	IdentityFile string   `json:"identity_file,omitempty"`
	ProxyJump    string   `json:"proxy_jump,omitempty"`
	SavePassword bool     `json:"save_password,omitempty"`
	LastLogin    int64    `json:"last_login,omitempty"`
}

type Store struct {
	Hosts []SSHHost `json:"hosts"`
}

func Dir() (string, error) {
	dir := strings.TrimSpace(os.Getenv("XSH_CONFIG_HOME"))
	if dir == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(base, "xsh")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

func Path() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "hosts.json"), nil
}

func Load() (*Store, error) {
	path, err := Path()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &Store{}, nil
	}
	if err != nil {
		return nil, err
	}
	var store Store
	if err := json.Unmarshal(data, &store); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	for i := range store.Hosts {
		store.Hosts[i].applyDefaults()
	}
	return &store, nil
}

func (s *Store) Save() error {
	path, err := Path()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (s *Store) Find(name string) *SSHHost {
	for i := range s.Hosts {
		if strings.EqualFold(s.Hosts[i].Name, name) {
			return &s.Hosts[i]
		}
	}
	return nil
}

func (s *Store) Add(host SSHHost) error {
	host.applyDefaults()
	if err := host.Validate(); err != nil {
		return err
	}
	if s.Find(host.Name) != nil {
		return fmt.Errorf("host %q already exists", host.Name)
	}
	s.Hosts = append(s.Hosts, host)
	return s.Save()
}

func (s *Store) Update(oldName string, host SSHHost) error {
	host.applyDefaults()
	if err := host.Validate(); err != nil {
		return err
	}
	if existing := s.Find(host.Name); existing != nil && !strings.EqualFold(oldName, host.Name) {
		return fmt.Errorf("host %q already exists", host.Name)
	}
	for i := range s.Hosts {
		if strings.EqualFold(s.Hosts[i].Name, oldName) {
			s.Hosts[i] = host
			return s.Save()
		}
	}
	return fmt.Errorf("host %q not found", oldName)
}

func (s *Store) Delete(name string) error {
	for i := range s.Hosts {
		if strings.EqualFold(s.Hosts[i].Name, name) {
			s.Hosts = append(s.Hosts[:i], s.Hosts[i+1:]...)
			return s.Save()
		}
	}
	return fmt.Errorf("host %q not found", name)
}

func (s *Store) RecordLogin(name string) error {
	host := s.Find(name)
	if host == nil {
		return nil
	}
	host.LastLogin = time.Now().Unix()
	return s.Save()
}

func (h *SSHHost) applyDefaults() {
	h.Name = strings.TrimSpace(h.Name)
	h.Hostname = strings.TrimSpace(h.Hostname)
	h.User = strings.TrimSpace(h.User)
	if h.Port == 0 {
		h.Port = 22
	}
	if h.AuthType == "" {
		h.AuthType = AuthAuto
	}
	sort.Strings(h.Tags)
}

func (h SSHHost) Validate() error {
	if h.Name == "" || h.Hostname == "" || h.User == "" {
		return errors.New("name, hostname and username are required")
	}
	if h.Port < 1 || h.Port > 65535 {
		return errors.New("port must be between 1 and 65535")
	}
	switch h.AuthType {
	case AuthAuto, AuthPassword, AuthPrivateKey:
	default:
		return fmt.Errorf("unsupported authentication type %q", h.AuthType)
	}
	if h.AuthType == AuthPrivateKey && h.IdentityFile == "" {
		return errors.New("identity file is required for private-key authentication")
	}
	return nil
}
