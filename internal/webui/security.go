package webui

import (
	"encoding/json"
	"github.com/zzqjlcn/xsh/internal/webstore"
	"net/http"
	"strings"
	"time"
)

func (s *server) securitySettings(w http.ResponseWriter, r *http.Request) {
	actor := requestSession(r)
	if r.Method != http.MethodGet && r.Method != http.MethodPut {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	hours, err := webstore.SessionHours(actor.User.ID)
	if err != nil {
		http.Error(w, "无法读取安全设置", 500)
		return
	}
	if r.Method == http.MethodPut {
		var input struct {
			SessionHours int `json:"session_hours"`
		}
		if !readJSON(w, r, &input) {
			return
		}
		if !webstore.ValidSessionHours(input.SessionHours) {
			http.Error(w, "不支持的登录有效期", 400)
			return
		}
		s.auth.Lock()
		if actor.Context.Err() != nil || !time.Now().Before(actor.expiresAt) {
			s.auth.Unlock()
			http.Error(w, "登录已失效", 401)
			return
		}
		if err := webstore.SetSessionHours(actor.User.ID, input.SessionHours); err != nil {
			s.auth.Unlock()
			http.Error(w, "无法保存安全设置", 500)
			return
		}
		hours = input.SessionHours
		s.resetSessionExpiryLocked(actor, time.Duration(hours)*time.Hour)
		s.auth.Unlock()
		secure := r.TLS != nil || sameOrigin(r) && strings.HasPrefix(r.Header.Get("Origin"), "https://")
		http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: actor.ID, Path: s.rootPath, HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: secure, MaxAge: hours * 3600})
	}
	s.auth.Lock()
	expires := actor.expiresAt.Unix()
	s.auth.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(struct {
		SessionHours int   `json:"session_hours"`
		ExpiresAt    int64 `json:"expires_at"`
	}{hours, expires})
}
