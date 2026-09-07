package copyui

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/zzqjlcn/xsh/internal/config"
	"github.com/zzqjlcn/xsh/internal/copyhistory"
	"github.com/zzqjlcn/xsh/internal/filecopy"
)

type Endpoint struct {
	HostName string
}

func (e Endpoint) IsLocal() bool { return e.HostName == "" }

func (e Endpoint) Label() string {
	if e.IsLocal() {
		return "当前主机 (local)"
	}
	return e.HostName
}

type Request struct {
	Source          Endpoint
	SourcePath      string
	Destination     Endpoint
	DestinationPath string
}

type Result struct {
	Request         *Request
	PrepareEndpoint *Endpoint
	Cancelled       bool
}

type step int

const (
	stepSource step = iota
	stepSourcePath
	stepDestination
	stepDestinationPath
	stepConfirm
)

type Wizard struct {
	endpoints      []Endpoint
	table          table.Model
	path           textinput.Model
	step           step
	request        Request
	result         Result
	files          map[string]*filecopy.FileSystem
	pending        step
	pathError      string
	candidates     []string
	candidateIndex int
	candidateValue string
	width          int
	height         int
	history        []copyhistory.Entry
}

var (
	accentStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))
	mutedStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	warnStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
)

func NewWizard(hosts []config.SSHHost, history []copyhistory.Entry) Wizard {
	copyHosts := append([]config.SSHHost(nil), hosts...)
	sort.Slice(copyHosts, func(i, j int) bool {
		return strings.ToLower(copyHosts[i].Name) < strings.ToLower(copyHosts[j].Name)
	})
	endpoints := []Endpoint{{}}
	rows := []table.Row{{"当前主机 (local)", "本机文件系统"}}
	for _, host := range copyHosts {
		endpoints = append(endpoints, Endpoint{HostName: host.Name})
		rows = append(rows, table.Row{host.Name, fmt.Sprintf("%s@%s:%d", host.User, host.Hostname, host.Port)})
	}
	t := table.New(
		table.WithColumns([]table.Column{{Title: "Endpoint", Width: 28}, {Title: "Address", Width: 44}}),
		table.WithRows(rows),
		table.WithFocused(true),
		table.WithHeight(12),
	)
	styles := table.DefaultStyles()
	styles.Selected = styles.Selected.Foreground(lipgloss.Color("230")).Background(lipgloss.Color("39")).Bold(false)
	t.SetStyles(styles)

	input := textinput.New()
	input.CharLimit = 4096
	input.Width = 72
	return Wizard{
		endpoints: endpoints,
		table:     t,
		path:      input,
		files:     map[string]*filecopy.FileSystem{"": filecopy.LocalFileSystem()},
		history:   append([]copyhistory.Entry(nil), history...),
	}
}

func (m Wizard) Init() tea.Cmd  { return nil }
func (m Wizard) Result() Result { return m.result }

func (m Wizard) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch message := message.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = message.Width, message.Height
		height := message.Height - 10 - len(m.history)
		if height < 3 {
			height = 3
		}
		m.table.SetHeight(height)
	case tea.KeyMsg:
		if message.String() == "ctrl+c" {
			m.result.Cancelled = true
			return m, tea.Quit
		}
		if m.step == stepSourcePath || m.step == stepDestinationPath {
			return m.updatePath(message)
		}
		switch message.String() {
		case "q":
			m.result.Cancelled = true
			return m, tea.Quit
		case "esc":
			switch m.step {
			case stepSource:
				m.result.Cancelled = true
				return m, tea.Quit
			case stepDestination:
				m.beginPath(stepSourcePath, m.request.SourcePath, "Source path")
			case stepConfirm:
				m.beginPath(stepDestinationPath, m.request.DestinationPath, "Destination path")
			}
			return m, nil
		case "enter":
			switch m.step {
			case stepSource:
				m.request.Source = m.selectedEndpoint()
				return m.enterPath(stepSourcePath, m.request.Source, m.request.SourcePath, "Source path")
			case stepDestination:
				m.request.Destination = m.selectedEndpoint()
				return m.enterPath(stepDestinationPath, m.request.Destination, m.request.DestinationPath, "Destination path")
			case stepConfirm:
				request := m.request
				m.result.Request = &request
				return m, tea.Quit
			}
			return m, nil
		}
	}
	var command tea.Cmd
	m.table, command = m.table.Update(message)
	return m, command
}

func (m Wizard) View() string {
	title := accentStyle.Render("xsh cp  Copy files")
	var body string
	switch m.step {
	case stepSource:
		body = m.renderHistory() + "\n\nSelect source\n\n" + m.table.View()
	case stepSourcePath:
		body = fmt.Sprintf("Source: %s\n\nSource path\n%s%s", accentStyle.Render(m.request.Source.Label()), m.path.View(), m.renderPathError())
	case stepDestination:
		body = fmt.Sprintf("Source: %s:%s\n\nSelect destination\n\n%s", m.request.Source.Label(), m.request.SourcePath, m.table.View())
	case stepDestinationPath:
		body = fmt.Sprintf("Source: %s:%s\nDestination: %s\n\nDestination path\n%s%s", m.request.Source.Label(), m.request.SourcePath, accentStyle.Render(m.request.Destination.Label()), m.path.View(), m.renderPathError())
	case stepConfirm:
		body = fmt.Sprintf(
			"Copy\n  %s:%s\n\nTo\n  %s:%s\n\n%s\n\nPress Enter to start",
			m.request.Source.Label(), m.request.SourcePath,
			m.request.Destination.Label(), m.request.DestinationPath,
			warnStyle.Render("Directories are copied recursively. Existing files may be overwritten."),
		)
	}
	help := mutedStyle.Render("↑/↓ or j/k select  Tab complete path  Enter next/confirm  Esc back  Ctrl+C cancel")
	return lipgloss.JoinVertical(lipgloss.Left, title, "", body, "", help)
}

func (m Wizard) updatePath(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	if key.String() == "tab" {
		return m.completePath()
	}
	m.resetCompletion()
	m.pathError = ""
	switch key.String() {
	case "esc":
		m.path.Blur()
		if m.step == stepSourcePath {
			m.step = stepSource
		} else {
			m.step = stepDestination
		}
		return m, nil
	case "enter":
		value := strings.TrimSpace(m.path.Value())
		if value == "" {
			return m, nil
		}
		m.path.Blur()
		if m.step == stepSourcePath {
			m.request.SourcePath = value
			m.step = stepDestination
			m.table.SetCursor(0)
		} else {
			m.request.DestinationPath = value
			m.step = stepConfirm
		}
		return m, nil
	}
	var command tea.Cmd
	m.path, command = m.path.Update(key)
	return m, command
}

func (m *Wizard) beginPath(next step, value, placeholder string) {
	m.step = next
	m.pathError = ""
	m.resetCompletion()
	m.path.SetValue(value)
	m.path.Placeholder = placeholder
	m.path.CursorEnd()
	m.path.Focus()
}

func (m Wizard) enterPath(next step, endpoint Endpoint, value, placeholder string) (tea.Model, tea.Cmd) {
	if m.files[endpoint.HostName] == nil {
		m.pending = next
		endpointCopy := endpoint
		m.result.PrepareEndpoint = &endpointCopy
		return m, tea.Quit
	}
	m.beginPath(next, value, placeholder)
	return m, nil
}

// SetFileSystem resumes a wizard after the caller prepares a remote endpoint.
func (m *Wizard) SetFileSystem(endpoint Endpoint, files *filecopy.FileSystem) {
	m.files[endpoint.HostName] = files
	m.result = Result{}
	if m.pending == stepSourcePath {
		m.beginPath(stepSourcePath, m.request.SourcePath, "Source path")
	} else {
		m.beginPath(stepDestinationPath, m.request.DestinationPath, "Destination path")
	}
}

func (m Wizard) completePath() (tea.Model, tea.Cmd) {
	endpoint := m.request.Source
	if m.step == stepDestinationPath {
		endpoint = m.request.Destination
	}
	files := m.files[endpoint.HostName]
	if files == nil {
		m.pathError = "Path completion is unavailable for this endpoint"
		return m, nil
	}
	if len(m.candidates) > 0 && m.path.Value() == m.candidateValue {
		m.candidateIndex = (m.candidateIndex + 1) % len(m.candidates)
	} else {
		candidates, err := files.Complete(context.Background(), m.path.Value())
		if err != nil {
			m.pathError = err.Error()
			m.resetCompletion()
			return m, nil
		}
		if len(candidates) == 0 {
			m.pathError = "No matching paths"
			m.resetCompletion()
			return m, nil
		}
		m.candidates = candidates
		m.candidateIndex = 0
	}
	m.pathError = ""
	m.path.SetValue(m.candidates[m.candidateIndex])
	m.path.CursorEnd()
	m.candidateValue = m.path.Value()
	return m, nil
}

func (m *Wizard) resetCompletion() {
	m.candidates = nil
	m.candidateIndex = 0
	m.candidateValue = ""
}

func (m Wizard) renderPathError() string {
	if m.pathError == "" {
		return ""
	}
	return "\n" + warnStyle.Render(m.pathError)
}

func (m Wizard) renderHistory() string {
	lines := []string{mutedStyle.Render("Recent copies (last 10)")}
	if len(m.history) == 0 {
		return strings.Join(append(lines, "  No copy history"), "\n")
	}
	limit := len(m.history)
	if limit > copyhistory.Limit {
		limit = copyhistory.Limit
	}
	for index, entry := range m.history[:limit] {
		when := entry.CompletedAt.Local().Format("01-02 15:04")
		width := m.width
		if width <= 0 {
			width = 80
		}
		locationWidth := (width - 22) / 2
		if locationWidth < 10 {
			locationWidth = 10
		}
		lines = append(lines, fmt.Sprintf(
			"%2d. %s  %s → %s",
			index+1,
			when,
			shortenMiddle(entry.Source, locationWidth),
			shortenMiddle(entry.Destination, locationWidth),
		))
	}
	return strings.Join(lines, "\n")
}

func shortenMiddle(value string, width int) string {
	characters := []rune(value)
	if len(characters) <= width {
		return value
	}
	if width <= 1 {
		return "…"
	}
	left := (width - 1) / 2
	right := width - left - 1
	return string(characters[:left]) + "…" + string(characters[len(characters)-right:])
}

func (m Wizard) selectedEndpoint() Endpoint {
	index := m.table.Cursor()
	if index < 0 || index >= len(m.endpoints) {
		return Endpoint{}
	}
	return m.endpoints[index]
}
