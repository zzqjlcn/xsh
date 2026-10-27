package webui

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/zzqjlcn/xsh/internal/webstore"
)

func readJSON(w http.ResponseWriter, r *http.Request, value any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(value) != nil || decoder.Decode(new(any)) != io.EOF {
		http.Error(w, "invalid request body", 400)
		return false
	}
	return true
}
func (s *server) users(w http.ResponseWriter, r *http.Request) {
	session := requestSession(r)
	if session == nil || !session.User.Admin {
		http.Error(w, "仅管理员可以管理账号。", 403)
		return
	}
	switch r.Method {
	case "GET":
		users, err := webstore.Users()
		if err != nil {
			http.Error(w, "无法读取账号列表", 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(users)
	case "POST":
		var input struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if !readJSON(w, r, &input) {
			return
		}
		user, key, err := webstore.CreateUser(input.Username, input.Password, false, nil)
		for i := range key {
			key[i] = 0
		}
		if err != nil {
			status := 400
			if errors.Is(err, webstore.ErrConflict) {
				status = 409
			}
			http.Error(w, err.Error(), status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(201)
		_ = json.NewEncoder(w).Encode(user)
	case "PUT":
		var input struct {
			ID       int64 `json:"id"`
			Disabled bool  `json:"disabled"`
		}
		if !readJSON(w, r, &input) {
			return
		}
		if err := webstore.SetDisabled(input.ID, input.Disabled); err != nil {
			http.Error(w, "无法修改该账号，管理员账号不可停用。", 400)
			return
		}
		if input.Disabled {
			s.revokeUser(input.ID)
		}
		w.WriteHeader(204)
	case "DELETE":
		var input struct {
			ID int64 `json:"id"`
		}
		if !readJSON(w, r, &input) {
			return
		}
		if err := webstore.DeleteUser(input.ID); err != nil {
			if errors.Is(err, webstore.ErrNotFound) {
				http.Error(w, "账号不存在或为管理员，无法删除。", 400)
			} else {
				http.Error(w, "删除账号失败，请重试。", 500)
			}
			return
		}
		s.revokeUser(input.ID)
		w.WriteHeader(204)
	default:
		w.WriteHeader(405)
	}
}
func (s *server) changeLoginPassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		w.WriteHeader(405)
		return
	}
	session := requestSession(r)
	if session == nil {
		http.Error(w, "authentication required", 401)
		return
	}
	var input struct {
		Current  string `json:"current_password"`
		Password string `json:"password"`
		Confirm  string `json:"confirm"`
	}
	if !readJSON(w, r, &input) {
		return
	}
	if input.Password != input.Confirm {
		http.Error(w, "两次输入的密码不一致。", 400)
		return
	}
	if err := webstore.ChangeLoginPassword(session.User, input.Current, input.Password, session.Key); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	s.revokeUser(session.User.ID)
	w.WriteHeader(204)
}
func (s *server) shares(w http.ResponseWriter, r *http.Request) {
	session := requestSession(r)
	if session == nil {
		http.Error(w, "authentication required", 401)
		return
	}
	if r.URL.Path == "/api/share-recipients" {
		if r.Method != http.MethodGet {
			w.WriteHeader(405)
			return
		}
		items, err := webstore.ShareRecipients(session.User.ID)
		if err != nil {
			http.Error(w, "无法读取接收账号", 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(items)
		return
	}
	switch r.Method {
	case "GET":
		items, err := webstore.Shares(session.User.ID)
		if err != nil {
			http.Error(w, "无法读取收件箱", 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(items)
	case "POST":
		var input struct {
			Name       string   `json:"name"`
			Recipients []string `json:"recipients"`
		}
		if !readJSON(w, r, &input) {
			return
		}
		if err := webstore.ShareHost(session.User.ID, input.Name, input.Recipients); err != nil {
			status := 400
			if errors.Is(err, webstore.ErrNotFound) {
				status = 404
			}
			http.Error(w, err.Error(), status)
			return
		}
		w.WriteHeader(204)
	case "DELETE":
		var input struct {
			ID int64 `json:"id"`
		}
		if !readJSON(w, r, &input) {
			return
		}
		if err := webstore.DeleteShare(session.User.ID, input.ID); err != nil {
			http.Error(w, "分享不存在", 404)
			return
		}
		w.WriteHeader(204)
	default:
		w.WriteHeader(405)
	}
}
