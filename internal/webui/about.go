package webui

import (
	"encoding/json"
	"net/http"
	"runtime"
	"runtime/debug"
)

// Version can be set at build time with -ldflags "-X github.com/zzqjlcn/xsh/internal/webui.Version=v1.0.0".
var Version = "dev"

type appInfo struct {
	Version    string `json:"version"`
	Revision   string `json:"revision"`
	CommitTime string `json:"commit_time"`
	Modified   bool   `json:"modified"`
	GoVersion  string `json:"go_version"`
	Platform   string `json:"platform"`
}

func (s *server) about(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	info := appInfo{Version: Version, GoVersion: runtime.Version(), Platform: runtime.GOOS + "/" + runtime.GOARCH}
	if build, ok := debug.ReadBuildInfo(); ok {
		if info.Version == "dev" && build.Main.Version != "" && build.Main.Version != "(devel)" {
			info.Version = build.Main.Version
		}
		for _, setting := range build.Settings {
			switch setting.Key {
			case "vcs.revision":
				info.Revision = setting.Value
			case "vcs.time":
				info.CommitTime = setting.Value
			case "vcs.modified":
				info.Modified = setting.Value == "true"
			}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(info)
}
