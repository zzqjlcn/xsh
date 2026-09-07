package copyui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/zzqjlcn/xsh/internal/config"
	"github.com/zzqjlcn/xsh/internal/copyhistory"
	"github.com/zzqjlcn/xsh/internal/filecopy"
)

func TestWizardBuildsLocalToRemoteRequest(t *testing.T) {
	model := NewWizard([]config.SSHHost{{Name: "dev", Hostname: "10.0.0.1", User: "root", Port: 22}}, nil)
	model = updateKey(model, tea.KeyEnter)
	model = typeText(model, "/tmp/source")
	model = updateKey(model, tea.KeyEnter)
	model = updateKey(model, tea.KeyDown)
	model = updateKey(model, tea.KeyEnter)
	if model.Result().PrepareEndpoint == nil || model.Result().PrepareEndpoint.HostName != "dev" {
		t.Fatalf("wizard did not request remote preparation: %#v", model.Result())
	}
	model.SetFileSystem(Endpoint{HostName: "dev"}, filecopy.LocalFileSystem())
	model = typeText(model, "/tmp/destination")
	model = updateKey(model, tea.KeyEnter)
	model = updateKey(model, tea.KeyEnter)

	result := model.Result()
	if result.Request == nil {
		t.Fatal("wizard did not return a request")
	}
	if !result.Request.Source.IsLocal() || result.Request.Destination.HostName != "dev" {
		t.Fatalf("unexpected endpoints: %#v", result.Request)
	}
	if result.Request.SourcePath != "/tmp/source" || result.Request.DestinationPath != "/tmp/destination" {
		t.Fatalf("unexpected paths: %#v", result.Request)
	}
}

func TestTabCyclesPathCandidates(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "alpha-dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "alpha.txt"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	model := NewWizard(nil, nil)
	model = updateKey(model, tea.KeyEnter)
	model = typeText(model, filepath.Join(root, "alp"))
	model = updateKey(model, tea.KeyTab)
	first := filepath.Join(root, "alpha-dir") + string(filepath.Separator)
	if model.path.Value() != first {
		t.Fatalf("first Tab = %q, want %q", model.path.Value(), first)
	}
	model = updateKey(model, tea.KeyTab)
	second := filepath.Join(root, "alpha.txt")
	if model.path.Value() != second {
		t.Fatalf("second Tab = %q, want %q", model.path.Value(), second)
	}
}

func TestWizardShowsCopyHistory(t *testing.T) {
	model := NewWizard(nil, []copyhistory.Entry{{
		Source:      "local:/tmp/source",
		Destination: "dev:/tmp/destination",
		CompletedAt: time.Date(2026, 8, 21, 10, 30, 0, 0, time.Local),
	}})
	view := model.View()
	if !strings.Contains(view, "Recent copies (last 10)") || !strings.Contains(view, "local:/tmp/source → dev:/tmp/destination") {
		t.Fatalf("history missing from wizard:\n%s", view)
	}
}

func updateKey(model Wizard, key tea.KeyType) Wizard {
	updated, _ := model.Update(tea.KeyMsg{Type: key})
	return updated.(Wizard)
}

func typeText(model Wizard, value string) Wizard {
	for _, character := range value {
		updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{character}})
		model = updated.(Wizard)
	}
	return model
}
