package webui

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"path"
	"strings"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// Upload over the already authenticated terminal connection, without retaining passwords.
func (s *server) upload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	connection, ok := s.uploads.Load(r.URL.Query().Get("session"))
	if !ok {
		http.Error(w, "终端连接已关闭，请重新连接后上传。", http.StatusConflict)
		return
	}
	name, directory := r.URL.Query().Get("name"), r.URL.Query().Get("directory")
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\\x00") || strings.ContainsRune(directory, 0) {
		http.Error(w, "文件名或目标目录无效。", http.StatusBadRequest)
		return
	}
	// File transfers can take longer than the ordinary API request deadlines.
	controller := http.NewResponseController(w)
	_ = controller.SetReadDeadline(time.Time{})
	_ = controller.SetWriteDeadline(time.Time{})
	client, err := sftp.NewClient(connection.(*ssh.Client))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer client.Close()
	stop := context.AfterFunc(r.Context(), func() { _ = client.Close() })
	defer stop()
	if directory == "" || directory == "~" {
		directory = "."
	}
	if strings.HasPrefix(directory, "~/") {
		directory = strings.TrimPrefix(directory, "~/")
	}
	info, err := client.Stat(directory)
	if err != nil || !info.IsDir() {
		http.Error(w, "目标目录不存在或不可访问。", http.StatusBadRequest)
		return
	}
	target := path.Join(directory, name)
	if _, err := client.Lstat(target); !os.IsNotExist(err) {
		http.Error(w, "目标文件已存在或不可访问。", http.StatusConflict)
		return
	}
	var secret [16]byte
	if _, err := rand.Read(secret[:]); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	temporary := path.Join(directory, ".xsh-upload-"+hex.EncodeToString(secret[:]))
	file, err := client.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
	if err != nil {
		http.Error(w, "无法创建文件（可能已存在或没有写入权限）："+err.Error(), http.StatusConflict)
		return
	}
	defer client.Remove(temporary)
	_, copyErr := io.Copy(file, r.Body)
	closeErr := file.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		http.Error(w, "上传失败："+err.Error(), http.StatusBadGateway)
		return
	}
	// Standard SFTP rename refuses an existing destination (unlike PosixRename).
	if err := client.Rename(temporary, target); err != nil {
		http.Error(w, "保存失败（目标可能已存在）："+err.Error(), http.StatusConflict)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
