package webui

import (
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"

	"github.com/zzqjlcn/xsh/internal/config"
	"github.com/zzqjlcn/xsh/internal/credential"
)

//go:embed static/*
var assets embed.FS

type server struct {
	mu        sync.Mutex
	token     string
	session   string
	sessionMu sync.RWMutex
	remote    bool
	uploads   sync.Map // connection ID -> *ssh.Client
}

const sessionCookie = "xsh_web_session"

func New() (http.Handler, error) {
	return NewForHost("127.0.0.1")
}

func NewForHost(bindHost string) (http.Handler, error) {
	var tokenSecret, sessionSecret [32]byte
	if _, err := rand.Read(tokenSecret[:]); err != nil {
		return nil, err
	}
	if _, err := rand.Read(sessionSecret[:]); err != nil {
		return nil, err
	}
	bindIP := net.ParseIP(bindHost)
	localOnly := strings.EqualFold(bindHost, "localhost") || bindIP != nil && bindIP.IsLoopback()
	s := &server{token: hex.EncodeToString(tokenSecret[:]), session: hex.EncodeToString(sessionSecret[:]), remote: !localOnly}
	return http.HandlerFunc(s.serve), nil
}

func (s *server) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
	host, _, err := net.SplitHostPort(r.Host)
	hostIP := net.ParseIP(host)
	loopbackHost := strings.EqualFold(host, "localhost") || hostIP != nil && hostIP.IsLoopback()
	if err != nil || !s.remote && !loopbackHost {
		http.Error(w, "use the printed loopback address", http.StatusForbidden)
		return
	}
	if origin := r.Header.Get("Origin"); origin != "" && origin != "http://"+r.Host {
		http.Error(w, "cross-origin requests are not allowed", http.StatusForbidden)
		return
	}
	if r.URL.Path == "/ws/terminal" {
		if !s.authenticated(r) {
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		s.terminal(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/") {
		if r.URL.Path == "/api/auth/status" {
			s.authStatus(w, r)
			return
		}
		if r.URL.Path == "/api/login" {
			s.login(w, r)
			return
		}
		if !s.authenticated(r) {
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/api/logout" {
			s.logout(w, r)
			return
		}
		if r.Method != http.MethodGet && subtle.ConstantTimeCompare([]byte(r.Header.Get("X-XSH-Token")), []byte(s.token)) != 1 {
			http.Error(w, "invalid request token", http.StatusForbidden)
			return
		}
		s.api(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/")
	if name == "" {
		name = "index.html"
	}
	allowed := map[string]bool{
		"index.html": true, "app.js": true, "style.css": true, "terminal.css": true, "state.css": true,
		"files.js": true, "files.css": true,
		"favicon.ico": true, "favicon.png": true, "logo.png": true,
		"vendor/xterm.js": true, "vendor/xterm.css": true, "vendor/addon-fit.js": true,
	}
	if !allowed[name] {
		http.NotFound(w, r)
		return
	}
	data, err := assets.ReadFile("static/" + name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	contentType := "text/javascript; charset=utf-8"
	if name == "index.html" {
		contentType = "text/html; charset=utf-8"
	}
	if name == "favicon.ico" {
		contentType = "image/x-icon"
	}
	if strings.HasSuffix(name, ".png") {
		contentType = "image/png"
	}
	if strings.HasSuffix(name, ".css") {
		contentType = "text/css; charset=utf-8"
	}
	w.Header().Set("Content-Type", contentType)
	if r.Method != http.MethodHead {
		_, _ = w.Write(data)
	}
}

func (s *server) authStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(struct {
		SetupRequired bool `json:"setup_required"`
		Authenticated bool `json:"authenticated"`
	}{!credential.VaultExists(), s.authenticated(r)})
}

func (s *server) authenticated(r *http.Request) bool {
	cookie, err := r.Cookie(sessionCookie)
	if err != nil {
		return false
	}
	s.sessionMu.RLock()
	defer s.sessionMu.RUnlock()
	return subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(s.session)) == 1
}

func (s *server) login(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var input struct {
		Password string `json:"password"`
		Create   bool   `json:"create"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	var err error
	if credential.VaultExists() {
		err = credential.UnlockVault(input.Password)
	} else if input.Create {
		err = credential.CreateVault(input.Password)
	} else {
		http.Error(w, "password vault setup is required", http.StatusConflict)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: s.session, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(struct {
		Token string `json:"token"`
	}{s.token})
}

func (s *server) logout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-XSH-Token")), []byte(s.token)) != 1 {
		http.Error(w, "invalid request", http.StatusForbidden)
		return
	}
	credential.LockVault()
	var next [32]byte
	if _, err := rand.Read(next[:]); err == nil {
		s.sessionMu.Lock()
		s.session = hex.EncodeToString(next[:])
		s.sessionMu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: -1})
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) api(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/api/files" || r.URL.Path == "/api/download" {
		s.remoteFiles(w, r)
		return
	}
	if r.URL.Path == "/api/upload" {
		s.upload(w, r)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.URL.Path != "/api/hosts" {
		http.NotFound(w, r)
		return
	}
	store, err := config.Load()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if r.Method == http.MethodGet {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(struct {
			Hosts []config.SSHHost `json:"hosts"`
			Token string           `json:"token"`
		}{store.Hosts, s.token})
		return
	}
	if r.Method != http.MethodPost && r.Method != http.MethodPut && r.Method != http.MethodDelete {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var input struct {
		Original string         `json:"original"`
		Host     config.SSHHost `json:"host"`
		Password string         `json:"password"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		http.Error(w, "invalid request body", 400)
		return
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		http.Error(w, "invalid request body", 400)
		return
	}
	current := store.Find(input.Original)
	if r.Method != http.MethodPost && current == nil {
		http.Error(w, "host not found", 404)
		return
	}
	switch r.Method {
	case http.MethodPost:
		if input.Host.SavePassword && input.Host.AuthType != config.AuthPassword {
			http.Error(w, "password can only be saved for password authentication", 400)
			return
		}
		if input.Host.SavePassword && input.Password == "" {
			http.Error(w, "host password is required when saving it", 400)
			return
		}
		input.Host.LastLogin = 0
		if err = input.Host.Validate(); err == nil && store.Find(input.Host.Name) != nil {
			err = fmt.Errorf("host %q already exists", input.Host.Name)
		}
		if err == nil && input.Host.SavePassword {
			err = credential.SetVaultPassword(input.Host.Name, input.Password, nil)
		}
		if err != nil {
			break
		}
		err = store.Add(input.Host)
	case http.MethodPut:
		// Credentials are keyed by alias; keep the alias stable in this editor.
		input.Host.Name = current.Name
		input.Host.LastLogin = current.LastLogin
		wantSavedPassword := input.Host.AuthType == config.AuthPassword && input.Host.SavePassword
		if wantSavedPassword && input.Password == "" && !current.SavePassword {
			http.Error(w, "host password is required when saving it", 400)
			return
		}
		if wantSavedPassword && input.Password != "" {
			err = credential.SetVaultPassword(current.Name, input.Password, nil)
		} else if !wantSavedPassword && current.SavePassword {
			err = credential.DeletePassword(current.Name, nil)
		}
		input.Host.SavePassword = wantSavedPassword
		if err != nil {
			break
		}
		err = store.Update(current.Name, input.Host)
	case http.MethodDelete:
		if current.SavePassword {
			err = credential.DeletePassword(current.Name, func(string) (string, error) {
				return "", errors.New("use the terminal UI to unlock the password vault and delete this host")
			})
			if err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
		}
		err = store.Delete(current.Name)
	}
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
