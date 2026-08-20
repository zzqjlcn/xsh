package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/zzqjlcn/xsh/internal/config"
)

func TestSearchFiltersHosts(t *testing.T) {
	model := New([]config.SSHHost{
		{Name: "development", Hostname: "10.0.0.1", User: "root"},
		{Name: "production", Hostname: "10.0.0.2", User: "deploy"},
	})
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	model = updated.(Model)
	for _, char := range "deploy" {
		updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{char}})
		model = updated.(Model)
	}
	if !strings.Contains(model.View(), "1 host(s)") {
		t.Fatalf("filtered view did not report one host:\n%s", model.View())
	}
}

func TestCopyCommand(t *testing.T) {
	model := New(nil)
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	model = updated.(Model)
	if !strings.Contains(model.View(), "Command:") {
		t.Fatal("copy command prefix was not shown")
	}
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	model = updated.(Model)
	if model.Result().Action != ActionCopy {
		t.Fatalf("copy command action = %v", model.Result().Action)
	}
}
