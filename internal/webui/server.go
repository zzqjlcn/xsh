package webui

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"path"
	"strings"
	"sync"

	"github.com/zzqjlcn/xsh/internal/config"
	"github.com/zzqjlcn/xsh/internal/webstore"
)

//go:embed static/*
var assets embed.FS

type server struct {
	mu              sync.Mutex
	auth            authSessions
	remote          bool
	rootPath        string
	passwordTargets sync.Map
	uploads         sync.Map // connection ID -> *webConnection
}

const sessionCookie = "xsh_web_session"

func New() (http.Handler, error) {
	return NewForHost("127.0.0.1")
}

func NewForHost(bindHost string) (http.Handler, error) {
	return NewForHostAndRootPath(bindHost, "/")
}

// NormalizeRootPath returns a canonical mount path with a trailing slash.
func NormalizeRootPath(root string) (string, error) {
	if root == "" || root == "/" {
		return "/", nil
	}
	if !strings.HasPrefix(root, "/") || strings.HasPrefix(root, "//") || strings.ContainsAny(root, "?#%\\") {
		return "", errors.New("root path must be an absolute URL path, such as /xsh")
	}
	for _, c := range root {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("/-._~", c)) {
			return "", errors.New("root path may contain only letters, digits, /, -, ., _, and ~")
		}
	}
	root = path.Clean(root)
	if root == "/" {
		return root, nil
	}
	return root + "/", nil
}

func NewForHostAndRootPath(bindHost, rootPath string) (http.Handler, error) {
	rootPath, err := NormalizeRootPath(rootPath)
	if err != nil {
		return nil, err
	}
	bindIP := net.ParseIP(bindHost)
	localOnly := strings.EqualFold(bindHost, "localhost") || bindIP != nil && bindIP.IsLoopback()
	s := &server{remote: !localOnly, rootPath: rootPath}
	handler := http.StripPrefix(strings.TrimSuffix(rootPath, "/"), http.HandlerFunc(s.serve))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rootPath != "/" && r.URL.Path == strings.TrimSuffix(rootPath, "/") {
			target := rootPath
			if r.URL.RawQuery != "" {
				target += "?" + r.URL.RawQuery
			}
			http.Redirect(w, r, target, http.StatusPermanentRedirect)
			return
		}
		if !strings.HasPrefix(r.URL.Path, rootPath) {
			http.NotFound(w, r)
			return
		}
		handler.ServeHTTP(w, r)
	}), nil
}

func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	return origin == "http://"+r.Host || origin == "https://"+r.Host
}

func (s *server) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
	host, _, err := net.SplitHostPort(r.Host)
	if err != nil {
		host = strings.Trim(r.Host, "[]")
	}
	hostIP := net.ParseIP(host)
	loopbackHost := strings.EqualFold(host, "localhost") || hostIP != nil && hostIP.IsLoopback()
	if !s.remote && !loopbackHost {
		http.Error(w, "use the printed loopback address", http.StatusForbidden)
		return
	}
	if origin := r.Header.Get("Origin"); origin != "" && !sameOrigin(r) {
		http.Error(w, "cross-origin requests are not allowed", http.StatusForbidden)
		return
	}
	if r.URL.Path == "/ws/terminal" {
		session := s.lookupSession(r)
		if session == nil {
			http.Error(w, "authentication required", 401)
			return
		}
		if s.sessionLocked(session) {
			http.Error(w, "屏幕已锁定，请先解锁。", http.StatusLocked)
			return
		}
		s.terminal(w, r.WithContext(context.WithValue(r.Context(), sessionContextKey{}, session)))
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
		session := s.lookupSession(r)
		if session == nil {
			http.Error(w, "authentication required", 401)
			return
		}
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		stop := context.AfterFunc(session.Context, cancel)
		defer stop()
		r = r.WithContext(context.WithValue(ctx, sessionContextKey{}, session))
		if r.URL.Path == "/api/logout" {
			s.logout(w, r)
			return
		}
		if r.Method != http.MethodGet && !subtleToken(r.Header.Get("X-XSH-Token"), session.Token) {
			http.Error(w, "invalid request token", 403)
			return
		}
		if r.URL.Path == "/api/lock" || r.URL.Path == "/api/unlock" {
			s.screenLock(w, r)
			return
		}
		if s.sessionLocked(session) {
			http.Error(w, "屏幕已锁定，请先解锁。", http.StatusLocked)
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

func (s *server) api(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/api/about" {
		s.about(w, r)
		return
	}
	if r.URL.Path == "/api/hosts/test" {
		s.testConnection(w, r)
		return
	}
	if r.URL.Path == "/api/password/current" {
		s.currentPassword(w, r)
		return
	}
	if r.URL.Path == "/api/password" {
		s.changePassword(w, r)
		return
	}
	if r.URL.Path == "/api/files" || r.URL.Path == "/api/download" {
		s.remoteFiles(w, r)
		return
	}
	if r.URL.Path == "/api/upload" {
		s.upload(w, r)
		return
	}
	if r.URL.Path == "/api/users" {
		s.users(w, r)
		return
	}
	if r.URL.Path == "/api/account/password" {
		s.changeLoginPassword(w, r)
		return
	}
	if r.URL.Path == "/api/shares" || r.URL.Path == "/api/share-recipients" {
		s.shares(w, r)
		return
	}
	if r.URL.Path != "/api/hosts" {
		http.NotFound(w, r)
		return
	}
	session := requestSession(r)
	if session == nil {
		http.Error(w, "authentication required", 401)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.Method == http.MethodGet {
		hosts, err := webstore.ListHosts(session.User.ID)
		if err != nil {
			http.Error(w, "无法读取主机列表", 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(struct {
			Hosts []webstore.Host `json:"hosts"`
			Token string          `json:"token"`
			User  webstore.User   `json:"user"`
		}{hosts, session.Token, session.User})
		return
	}
	if r.Method != "POST" && r.Method != "PUT" && r.Method != "DELETE" {
		w.WriteHeader(405)
		return
	}
	var input struct {
		Original   string         `json:"original"`
		Host       config.SSHHost `json:"host"`
		Password   string         `json:"password"`
		PrivateKey string         `json:"private_key"`
		ShareID    int64          `json:"share_id"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 128<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF {
		http.Error(w, "invalid request body", 400)
		return
	}
	var err error
	if r.Method == "DELETE" {
		err = webstore.DeleteHost(session.User.ID, input.Original)
	} else {
		if input.Host.IdentityFile != "" {
			http.Error(w, "请选择本地私钥文件或粘贴内容，不能使用服务端文件路径。", 400)
			return
		}
		if r.Method == "PUT" {
			if input.Host.Name == "" {
				input.Host.Name = input.Original
			}
			err = webstore.UpdateHost(session.User.ID, session.Key, input.Original, input.Host, webstore.Secret{Password: input.Password, PrivateKey: input.PrivateKey})
		} else {
			err = webstore.SaveHost(session.User.ID, session.Key, input.Host, webstore.Secret{Password: input.Password, PrivateKey: input.PrivateKey}, false, input.ShareID)
		}
	}
	if err != nil {
		status := 400
		if errors.Is(err, webstore.ErrNotFound) {
			status = 404
		}
		if errors.Is(err, webstore.ErrConflict) {
			status = 409
		}
		http.Error(w, err.Error(), status)
		return
	}
	w.WriteHeader(204)
}
