package webui

import (
	"context"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pkg/sftp"
)

type remoteEntry struct {
	Name      string `json:"name"`
	Size      int64  `json:"size"`
	Directory bool   `json:"directory"`
	Regular   bool   `json:"regular"`
	Modified  int64  `json:"modified"`
}

func (s *server) remoteFiles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	connection, ok := s.uploads.Load(r.URL.Query().Get("session"))
	if !ok {
		http.Error(w, "终端连接已关闭，请重新连接。", http.StatusConflict)
		return
	}
	target := connection.(*webConnection)
	if !ownedConnection(requestSession(r), target.UserID, target.SessionID) {
		http.Error(w, "connection not found", http.StatusNotFound)
		return
	}
	value := r.URL.Query().Get("path")
	cwdUnavailable := false
	if r.URL.Path == "/api/files" && r.URL.Query().Get("cwd") == "1" {
		if directory, err := terminalWorkingDirectory(r.Context(), target.Client); err == nil {
			value = directory
		} else {
			value = "."
			cwdUnavailable = true
		}
	}
	if strings.ContainsRune(value, 0) {
		http.Error(w, "路径无效。", http.StatusBadRequest)
		return
	}
	client, err := sftp.NewClient(target.Client)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer client.Close()
	stop := context.AfterFunc(r.Context(), func() { _ = client.Close() })
	defer stop()
	if value == "" || value == "~" {
		value = "."
	}
	if strings.HasPrefix(value, "~/") {
		value = strings.TrimPrefix(value, "~/")
	}
	if r.URL.Path == "/api/files" {
		resolved, err := client.RealPath(value)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		items, err := client.ReadDirContext(r.Context(), resolved)
		if err != nil {
			http.Error(w, "无法读取目录："+err.Error(), http.StatusBadRequest)
			return
		}
		entries := make([]remoteEntry, 0, len(items))
		for _, item := range items {
			if item.Name() == "." || item.Name() == ".." {
				continue
			}
			info := item
			if item.Mode()&os.ModeSymlink != 0 {
				if target, err := client.Stat(path.Join(resolved, item.Name())); err == nil {
					info = target
				}
			}
			entries = append(entries, remoteEntry{item.Name(), info.Size(), info.IsDir(), info.Mode().IsRegular(), info.ModTime().UnixMilli()})
		}
		sort.Slice(entries, func(i, j int) bool {
			if entries[i].Directory != entries[j].Directory {
				return entries[i].Directory
			}
			return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
		})
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(struct {
			Path           string        `json:"path"`
			Entries        []remoteEntry `json:"entries"`
			CWDUnavailable bool          `json:"cwd_unavailable,omitempty"`
		}{resolved, entries, cwdUnavailable})
		return
	}
	info, err := client.Stat(value)
	if err != nil {
		http.Error(w, "无法读取文件："+err.Error(), http.StatusNotFound)
		return
	}
	if !info.Mode().IsRegular() {
		http.Error(w, "请选择普通文件下载。", http.StatusBadRequest)
		return
	}
	file, err := client.Open(value)
	if err != nil {
		http.Error(w, "无法打开文件："+err.Error(), http.StatusNotFound)
		return
	}
	defer file.Close()
	info, err = file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		http.Error(w, "请选择普通文件下载。", http.StatusBadRequest)
		return
	}
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": path.Base(value)}))
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	if _, err := io.CopyN(w, file, info.Size()); err != nil {
		// Abort the response so a truncated transfer is never treated as successful.
		panic(http.ErrAbortHandler)
	}
}
