package webui

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/zzqjlcn/xsh/internal/config"
	"github.com/zzqjlcn/xsh/internal/sshclient"
	"github.com/zzqjlcn/xsh/internal/webstore"
	"golang.org/x/crypto/ssh"
)

type passwordTarget struct {
	mu        sync.Mutex
	userID    int64
	sessionID string
	host      config.SSHHost
	client    *ssh.Client
}

func (s *server) savedPasswordForHost(actor *webSession, target config.SSHHost) (string, error) {
	if actor == nil {
		return "", webstore.ErrNotFound
	}
	host, err := webstore.FindHost(actor.User.ID, target.Name)
	if errors.Is(err, webstore.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if host.Hostname != target.Hostname || host.User != target.User || host.Port != target.Port || !host.SavePassword {
		return "", nil
	}
	secret, err := webstore.ReadSecret(actor.User.ID, actor.Key, host.Name)
	return secret.Password, err
}

// Only explicit, authenticated actions may reveal a saved credential. Reuse the
// same connection/host identity checks as password changes, and never cache it.
func (s *server) currentPassword(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var input struct {
		Session string `json:"session"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	value, ok := s.passwordTargets.Load(input.Session)
	if !ok {
		http.Error(w, "终端连接已关闭，请重新连接。", http.StatusConflict)
		return
	}
	target := value.(*passwordTarget)
	actor := requestSession(r)
	if !ownedConnection(actor, target.userID, target.sessionID) {
		http.Error(w, "connection not found", 404)
		return
	}
	if !target.mu.TryLock() {
		http.Error(w, "该连接正在修改密码，请等待完成。", http.StatusConflict)
		return
	}
	defer target.mu.Unlock()
	password, err := s.savedPasswordForHost(actor, target.host)
	if err != nil {
		http.Error(w, "无法读取已保存的密码，请重新解锁密码本。", http.StatusBadRequest)
		return
	}
	if password == "" {
		http.Error(w, "该连接没有可用的已保存密码，请手动输入当前密码。", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(struct {
		Password string `json:"password"`
	}{password})
}

func (s *server) changePassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var input struct {
		Session  string `json:"session"`
		Current  string `json:"current_password"`
		Password string `json:"password"`
		Confirm  string `json:"confirm"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || input.Password != input.Confirm || len([]rune(input.Password)) < 8 || len(input.Password) > 1024 || input.Password == input.Current {
		http.Error(w, "新密码至少 8 个字符、两次输入须一致，且不能与当前密码相同。", http.StatusBadRequest)
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	value, ok := s.passwordTargets.Load(input.Session)
	if !ok {
		http.Error(w, "终端连接已关闭，请重新连接。", http.StatusConflict)
		return
	}
	target := value.(*passwordTarget)
	actor := requestSession(r)
	if !ownedConnection(actor, target.userID, target.sessionID) {
		http.Error(w, "connection not found", 404)
		return
	}
	if !target.mu.TryLock() {
		http.Error(w, "该连接正在修改密码，请等待完成。", http.StatusConflict)
		return
	}
	defer target.mu.Unlock()
	if input.Current == "" {
		// Resolve server-side, without exposing the saved password to the browser.
		// An alias may have been edited since this terminal was connected: never
		// send a different host/user's credential down the existing connection.
		var err error
		input.Current, err = s.savedPasswordForHost(actor, target.host)
		if err != nil {
			http.Error(w, "无法读取已保存的密码，请手动输入当前用户密码。", http.StatusBadRequest)
			return
		}
	}
	if target.host.User != "root" && input.Current == "" {
		http.Error(w, "没有可用的已保存密码，请输入远程当前用户的密码。", http.StatusBadRequest)
		return
	}
	if input.Password == input.Current {
		http.Error(w, "新密码不能与当前密码相同。", http.StatusBadRequest)
		return
	}
	// Leave time to send an error after the SSH timeout. The ordinary server
	// WriteTimeout is also 30s and would discard that response.
	controller := http.NewResponseController(w)
	if err := controller.SetWriteDeadline(time.Now().Add(45 * time.Second)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		http.Error(w, "无法设置密码修改请求的响应期限。", http.StatusInternalServerError)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := sshclient.ChangePassword(ctx, target.client, input.Current, input.Password); err != nil {
		status := http.StatusBadGateway
		message := "远程密码修改失败：请检查当前密码及服务器密码策略。"
		if errors.Is(err, sshclient.ErrCurrentPasswordRequired) {
			status, message = http.StatusBadRequest, "服务器要求当前用户密码，请填写后再提交。"
		} else if errors.Is(err, sshclient.ErrPasswordChangeUncertain) {
			status, message = http.StatusGatewayTimeout, "远程密码修改超时，结果尚未确认。请先验证远程密码状态，勿直接重复提交。"
		}
		http.Error(w, message, status)
		return
	}
	// The remote operation cannot be rolled back if updating local storage fails.
	// Return an explicit success with a warning, so the user never repeats passwd.
	message := "远程密码已修改。"
	saved := false
	host, err := webstore.FindHost(actor.User.ID, target.host.Name)
	if err == nil && host.Hostname == target.host.Hostname && host.User == target.host.User && host.Port == target.host.Port && host.SavePassword {
		err = webstore.UpdatePassword(actor.User.ID, actor.Key, target.host, input.Password)
		saved = err == nil
		if saved {
			message = "远程密码已修改，已同步更新保存的密码。"
		}
	}
	if err != nil {
		message = "远程密码已修改，但本地凭据更新失败。请保留新密码并手动更新主机配置。"
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(struct {
		Changed bool   `json:"changed"`
		Saved   bool   `json:"saved"`
		Message string `json:"message"`
	}{true, saved, message})
}
