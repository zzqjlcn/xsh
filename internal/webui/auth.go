package webui

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/zzqjlcn/xsh/internal/config"
	"github.com/zzqjlcn/xsh/internal/credential"
	"github.com/zzqjlcn/xsh/internal/webstore"
	"golang.org/x/crypto/ssh"
)

type sessionContextKey struct{}
type webSession struct {
	ID, Token string
	User      webstore.User
	Key       []byte
	Context   context.Context
	cancel    context.CancelFunc
	locked    bool // protected by server.auth
}

func (s *server) sessionLocked(session *webSession) bool {
	s.auth.Lock()
	defer s.auth.Unlock()
	return session.locked
}

func (s *server) screenLock(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	session := requestSession(r)
	if session == nil {
		http.Error(w, "authentication required", 401)
		return
	}
	if r.URL.Path == "/api/unlock" {
		var input struct {
			Password string `json:"password"`
		}
		if !readJSON(w, r, &input) {
			return
		}
		now := time.Now()
		attemptKey := "unlock:" + session.ID
		s.auth.Lock()
		attempt := s.auth.attempts[attemptKey]
		if now.After(attempt.Until) {
			attempt = loginAttempt{Until: now.Add(5 * time.Minute)}
		}
		if attempt.Count >= 10 {
			s.auth.Unlock()
			http.Error(w, "尝试过多，请稍后再试。", 429)
			return
		}
		if s.auth.attempts == nil {
			s.auth.attempts = map[string]loginAttempt{}
		}
		attempt.Count++
		s.auth.attempts[attemptKey] = attempt
		s.auth.Unlock()
		user, key, err := webstore.Authenticate(session.User.Username, input.Password)
		defer func() {
			for i := range key {
				key[i] = 0
			}
		}()
		if err != nil || user.ID != session.User.ID || subtle.ConstantTimeCompare(key, session.Key) != 1 {
			http.Error(w, "登录密码错误。", 403)
			return
		}
		if session.Context.Err() != nil {
			http.Error(w, "登录已失效，请重新登录。", 401)
			return
		}
		s.auth.Lock()
		session.locked = false
		delete(s.auth.attempts, attemptKey)
		s.auth.Unlock()
	} else {
		s.auth.Lock()
		session.locked = true
		s.auth.Unlock()
	}
	w.WriteHeader(http.StatusNoContent)
}

type loginAttempt struct {
	Count int
	Until time.Time
}
type authSessions struct {
	sync.Mutex
	sessions map[string]*webSession
	attempts map[string]loginAttempt
}

func requestSession(r *http.Request) *webSession {
	session, _ := r.Context().Value(sessionContextKey{}).(*webSession)
	return session
}
func randomToken() (string, error) {
	var data [32]byte
	_, err := rand.Read(data[:])
	return hex.EncodeToString(data[:]), err
}

func (s *server) lookupSession(r *http.Request) *webSession {
	cookie, err := r.Cookie(sessionCookie)
	if err != nil {
		return nil
	}
	s.auth.Lock()
	session := s.auth.sessions[cookie.Value]
	s.auth.Unlock()
	if session == nil {
		return nil
	}
	if session.Context.Err() != nil || !webstore.SessionValid(session.User) {
		s.revokeSession(session)
		return nil
	}
	return session
}
func (s *server) authenticated(r *http.Request) bool { return s.lookupSession(r) != nil }
func (s *server) newSession(user webstore.User, key []byte) (*webSession, error) {
	id, err := randomToken()
	if err != nil {
		return nil, err
	}
	token, err := randomToken()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Hour)
	session := &webSession{ID: id, Token: token, User: user, Key: key, Context: ctx, cancel: cancel}
	s.auth.Lock()
	defer s.auth.Unlock()
	if s.auth.sessions == nil {
		s.auth.sessions = map[string]*webSession{}
	}
	for id, old := range s.auth.sessions {
		if old.Context.Err() != nil {
			delete(s.auth.sessions, id)
		}
	}
	s.auth.sessions[id] = session
	return session, nil
}
func (s *server) revokeSession(session *webSession) {
	s.auth.Lock()
	delete(s.auth.sessions, session.ID)
	s.auth.Unlock()
	session.cancel()
	s.uploads.Range(func(_, value any) bool {
		connection := value.(*webConnection)
		if connection.SessionID == session.ID && connection.Client != nil {
			_ = connection.Client.Close()
		}
		return true
	})
}
func (s *server) revokeUser(id int64) {
	s.auth.Lock()
	var sessions []*webSession
	for _, session := range s.auth.sessions {
		if session.User.ID == id {
			sessions = append(sessions, session)
		}
	}
	s.auth.Unlock()
	for _, session := range sessions {
		s.revokeSession(session)
	}
}

func (s *server) authStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	hasUsers, err := webstore.HasUsers()
	if err != nil {
		http.Error(w, "无法读取用户配置", 500)
		return
	}
	var user *webstore.User
	locked, token := false, ""
	session := s.lookupSession(r)
	if session != nil {
		u := session.User
		user = &u
		locked = s.sessionLocked(session)
		token = session.Token
	}
	legacyAvailable, legacyVault := false, false
	if !hasUsers {
		if store, err := config.Load(); err == nil {
			legacyAvailable = len(store.Hosts) > 0
		}
		legacyVault = legacyAvailable && credential.VaultExists()
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(struct {
		SetupRequired   bool           `json:"setup_required"`
		Authenticated   bool           `json:"authenticated"`
		User            *webstore.User `json:"user,omitempty"`
		LegacyAvailable bool           `json:"legacy_available"`
		LegacyVault     bool           `json:"legacy_vault"`
		Locked          bool           `json:"locked"`
		Token           string         `json:"token,omitempty"`
	}{!hasUsers, session != nil, user, legacyAvailable, legacyVault, locked, token})
}
func (s *server) login(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var input struct {
		Username       string `json:"username"`
		Password       string `json:"password"`
		Create         bool   `json:"create"`
		SetupCode      string `json:"setup_code"`
		ImportLegacy   bool   `json:"import_legacy"`
		LegacyPassword string `json:"legacy_password"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF {
		http.Error(w, "invalid request body", 400)
		return
	}
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		ip = r.RemoteAddr
	}
	now := time.Now()
	s.auth.Lock()
	if s.auth.attempts == nil {
		s.auth.attempts = map[string]loginAttempt{}
	}
	for address, item := range s.auth.attempts {
		if now.After(item.Until) {
			delete(s.auth.attempts, address)
		}
	}
	attempt := s.auth.attempts[ip]
	if attempt.Count >= 10 || len(s.auth.attempts) > 10000 {
		s.auth.Unlock()
		http.Error(w, "登录尝试过多，请稍后再试。", 429)
		return
	}
	attempt.Count++
	if attempt.Until.IsZero() {
		attempt.Until = now.Add(5 * time.Minute)
	}
	s.auth.attempts[ip] = attempt
	s.auth.Unlock()
	hasUsers, err := webstore.HasUsers()
	if err != nil {
		http.Error(w, "无法读取用户配置", 500)
		return
	}
	var user webstore.User
	var key []byte
	if !hasUsers {
		if !input.Create {
			http.Error(w, "请先创建管理员账号。", 409)
			return
		}
		code, codeErr := webstore.SetupCode()
		if codeErr != nil {
			http.Error(w, "无法读取初始化码", 500)
			return
		}
		if subtle.ConstantTimeCompare([]byte(input.SetupCode), []byte(code)) != 1 {
			http.Error(w, "初始化码错误，请查看 xsh web 启动终端。", 403)
			return
		}
		var initial []webstore.InitialHost
		if input.ImportLegacy {
			initial, err = legacyWebHosts(input.LegacyPassword)
			if err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
		}
		user, key, err = webstore.CreateUser(input.Username, input.Password, true, initial)
	} else if input.Create {
		http.Error(w, "管理员已创建，请登录。", 409)
		return
	} else {
		user, key, err = webstore.Authenticate(input.Username, input.Password)
	}
	if err != nil {
		http.Error(w, err.Error(), 401)
		return
	}
	session, err := s.newSession(user, key)
	if err != nil {
		http.Error(w, "创建会话失败", 500)
		return
	}
	s.auth.Lock()
	delete(s.auth.attempts, ip)
	s.auth.Unlock()
	secure := r.TLS != nil || sameOrigin(r) && strings.HasPrefix(r.Header.Get("Origin"), "https://")
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: session.ID, Path: s.rootPath, HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: secure, MaxAge: 12 * 60 * 60})
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(struct {
		Token string        `json:"token"`
		User  webstore.User `json:"user"`
	}{session.Token, user})
}
func (s *server) logout(w http.ResponseWriter, r *http.Request) {
	session := requestSession(r)
	if r.Method != http.MethodPost || session == nil || !subtleToken(r.Header.Get("X-XSH-Token"), session.Token) {
		http.Error(w, "invalid request", 403)
		return
	}
	s.revokeSession(session)
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: s.rootPath, HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: -1})
	w.WriteHeader(204)
}

func legacyWebHosts(master string) ([]webstore.InitialHost, error) {
	store, err := config.Load()
	if err != nil {
		return nil, err
	}
	if len(store.Hosts) > 0 && credential.VaultExists() {
		if err := credential.UnlockVault(master); err != nil {
			return nil, errors.New("原密码本主密码错误，或无法读取原密码本")
		}
		defer credential.LockVault()
	}
	result := []webstore.InitialHost{}
	for _, host := range store.Hosts {
		secret := webstore.Secret{}
		if host.SavePassword {
			secret.Password, err = credential.GetPassword(host.Name, nil)
			if err != nil {
				return nil, fmt.Errorf("无法读取 %s 的原密码，可取消导入后创建账号", host.Name)
			}
		}
		if host.AuthType == config.AuthPrivateKey {
			filename := host.IdentityFile
			if strings.HasPrefix(filename, "~/") || strings.HasPrefix(filename, `~\`) {
				home, err := os.UserHomeDir()
				if err != nil {
					return nil, err
				}
				filename = filepath.Join(home, filename[2:])
			}
			data, err := os.ReadFile(filename)
			if err != nil {
				return nil, fmt.Errorf("无法读取 %s 的原私钥，可取消导入后创建账号", host.Name)
			}
			secret.PrivateKey = string(data)
		}
		if host.AuthType == config.AuthPassword && secret.Password == "" {
			host.AuthType = config.AuthAuto
		}
		result = append(result, webstore.InitialHost{Host: host, Secret: secret})
	}
	return result, nil
}

type webConnection struct {
	UserID    int64
	SessionID string
	Client    *ssh.Client
}

func ownedConnection(session *webSession, userID int64, sessionID string) bool {
	return session != nil && session.Context.Err() == nil && session.User.ID == userID && session.ID == sessionID
}
