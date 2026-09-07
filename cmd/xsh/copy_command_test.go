package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zzqjlcn/xsh/internal/config"
	"github.com/zzqjlcn/xsh/internal/copyhistory"
)

func TestParseCopyLocation(t *testing.T) {
	store := &config.Store{Hosts: []config.SSHHost{{Name: "dev"}}}
	tests := []struct {
		name    string
		value   string
		host    string
		path    string
		wantErr string
	}{
		{name: "remote", value: "dev:/var/log/app.log", host: "dev", path: "/var/log/app.log"},
		{name: "explicit local", value: "local:/tmp/file", path: "/tmp/file"},
		{name: "windows drive", value: `C:\work\file.txt`, path: `C:\work\file.txt`},
		{name: "ordinary local", value: "notes:file.txt", path: "notes:file.txt"},
		{name: "unknown remote", value: "missing:/tmp/file", wantErr: `saved host "missing" not found`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			endpoint, path, err := parseCopyLocation(store, test.value)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("parseCopyLocation() error = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if endpoint.HostName != test.host || path != test.path {
				t.Fatalf("parseCopyLocation() = %q, %q", endpoint.HostName, path)
			}
		})
	}
}

func TestCompleteHostsFiltersAndSortsAliases(t *testing.T) {
	store := &config.Store{Hosts: []config.SSHHost{{Name: "prod"}, {Name: "Dev-2"}, {Name: "dev-1"}}}
	candidates := completeHosts(store, "DE")
	want := []string{"dev-1:", "Dev-2:"}
	if len(candidates) != len(want) || candidates[0] != want[0] || candidates[1] != want[1] {
		t.Fatalf("completeHosts() = %#v, want %#v", candidates, want)
	}
	if candidates := completeHosts(store, "dev-1:/tmp"); len(candidates) != 0 {
		t.Fatalf("path unexpectedly returned host candidates: %#v", candidates)
	}
}

func TestRunCopyCommandCopiesLocallyAndRecordsHistory(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("XSH_CONFIG_HOME", configDir)
	root := t.TempDir()
	source := filepath.Join(root, "source.txt")
	destination := filepath.Join(root, "destination.txt")
	if err := os.WriteFile(source, []byte("command copy"), 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := runCopyCommand(&config.Store{}, []string{source, destination}, &output); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "command copy" || !strings.Contains(output.String(), "Copy complete") {
		t.Fatalf("destination = %q, output = %q", data, output.String())
	}
	history, err := copyhistory.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 || history[0].Source != "local:"+source || history[0].Destination != "local:"+destination {
		t.Fatalf("history = %#v", history)
	}
}

func TestCompletionScriptsUseSavedHostProvider(t *testing.T) {
	for _, shell := range []string{"bash", "zsh", "powershell"} {
		script, err := completionScript(shell)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(script, "__complete-hosts") {
			t.Fatalf("%s completion does not use saved hosts", shell)
		}
	}
}
