package ui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/zzqjlcn/xsh/internal/config"
)

type Action int

const (
	ActionNone Action = iota
	ActionQuit
	ActionConnect
	ActionAdd
	ActionEdit
	ActionDelete
	ActionCopy
)

type Result struct {
	Action Action
	Host   *config.SSHHost
}

type Model struct {
	hosts        []config.SSHHost
	filtered     []config.SSHHost
	table        table.Model
	search       textinput.Model
	searchActive bool
	command      string
	sortRecent   bool
	width        int
	height       int
	result       Result
}

var (
	primary    = lipgloss.Color("39")
	muted      = lipgloss.Color("241")
	titleStyle = lipgloss.NewStyle().Bold(true).Foreground(primary)
	helpStyle  = lipgloss.NewStyle().Foreground(muted)
	infoStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
)

func New(hosts []config.SSHHost) Model {
	copyHosts := append([]config.SSHHost(nil), hosts...)
	t := table.New(table.WithFocused(true), table.WithHeight(12))
	styles := table.DefaultStyles()
	styles.Header = styles.Header.Foreground(lipgloss.Color("252")).BorderStyle(lipgloss.NormalBorder()).BorderForeground(lipgloss.Color("240")).BorderBottom(true).Bold(false)
	styles.Selected = styles.Selected.Foreground(lipgloss.Color("230")).Background(primary).Bold(false)
	t.SetStyles(styles)

	search := textinput.New()
	search.Placeholder = "Search hosts, users or tags..."
	search.CharLimit = 96
	search.Width = 42

	m := Model{hosts: copyHosts, table: t, search: search}
	m.resizeTable()
	m.applyFilter()
	return m
}

func (m Model) Init() tea.Cmd  { return nil }
func (m Model) Result() Result { return m.result }

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.resizeTable()
	case tea.KeyMsg:
		if m.searchActive {
			switch msg.String() {
			case "esc":
				m.search.SetValue("")
				m.applyFilter()
				m.searchActive = false
				m.search.Blur()
				return m, nil
			case "enter":
				m.searchActive = false
				m.search.Blur()
				return m, nil
			}
			var cmd tea.Cmd
			m.search, cmd = m.search.Update(msg)
			m.applyFilter()
			return m, cmd
		}
		if m.command == "c" {
			m.command = ""
			if msg.String() == "p" {
				m.result.Action = ActionCopy
				return m, tea.Quit
			}
		}

		switch msg.String() {
		case "q", "ctrl+c":
			m.result.Action = ActionQuit
			return m, tea.Quit
		case "/":
			m.searchActive = true
			m.search.Focus()
			return m, textinput.Blink
		case "a":
			m.result.Action = ActionAdd
			return m, tea.Quit
		case "e":
			return m.finishWith(ActionEdit)
		case "d":
			return m.finishWith(ActionDelete)
		case "c":
			m.command = "c"
			return m, nil
		case "enter":
			return m.finishWith(ActionConnect)
		case "j":
			m.table.MoveDown(1)
			return m, nil
		case "k":
			m.table.MoveUp(1)
			return m, nil
		case "s":
			m.sortRecent = !m.sortRecent
			m.applyFilter()
			return m, nil
		}
	}
	var cmd tea.Cmd
	m.table, cmd = m.table.Update(msg)
	return m, cmd
}

func (m Model) View() string {
	title := titleStyle.Render("xsh  SSH Manager")
	search := helpStyle.Render("Press / to search")
	if m.command != "" {
		search = "Command: " + titleStyle.Render(m.command)
	}
	if m.searchActive || m.search.Value() != "" {
		search = "Search: " + m.search.View()
	}
	sortMode := "name"
	if m.sortRecent {
		sortMode = "recent"
	}
	status := infoStyle.Render(fmt.Sprintf("%d host(s)  ·  sorted by %s", len(m.filtered), sortMode))
	help := helpStyle.Render("↑/↓ or j/k navigate  enter connect  a add  e edit  d delete  cp copy  / search  s sort  q quit")
	return lipgloss.JoinVertical(lipgloss.Left, title, "", search, "", m.table.View(), "", status, help)
}

func (m Model) finishWith(action Action) (tea.Model, tea.Cmd) {
	host := m.selected()
	if host == nil {
		return m, nil
	}
	m.result = Result{Action: action, Host: host}
	return m, tea.Quit
}

func (m Model) selected() *config.SSHHost {
	if len(m.filtered) == 0 {
		return nil
	}
	index := m.table.Cursor()
	if index < 0 || index >= len(m.filtered) {
		return nil
	}
	host := m.filtered[index]
	return &host
}

func (m *Model) applyFilter() {
	query := strings.ToLower(strings.TrimSpace(m.search.Value()))
	m.filtered = m.filtered[:0]
	for _, host := range m.hosts {
		value := strings.ToLower(host.Name + " " + host.Hostname + " " + host.User + " " + strings.Join(host.Tags, " "))
		if query == "" || strings.Contains(value, query) {
			m.filtered = append(m.filtered, host)
		}
	}
	sort.SliceStable(m.filtered, func(i, j int) bool {
		if m.sortRecent && m.filtered[i].LastLogin != m.filtered[j].LastLogin {
			return m.filtered[i].LastLogin > m.filtered[j].LastLogin
		}
		return strings.ToLower(m.filtered[i].Name) < strings.ToLower(m.filtered[j].Name)
	})
	m.updateRows()
}

func (m *Model) updateRows() {
	rows := make([]table.Row, 0, len(m.filtered))
	for _, host := range m.filtered {
		lock := ""
		if host.SavePassword {
			lock = " 🔐"
		}
		rows = append(rows, table.Row{"● " + host.Name + lock, host.Hostname, host.User, strings.Join(host.Tags, " "), formatLastLogin(host.LastLogin)})
	}
	m.table.SetRows(rows)
	m.resizeTable()
}

func (m *Model) resizeTable() {
	width := m.width
	if width <= 0 {
		width = 100
	}
	nameWidth := clamp(width*24/100, 14, 28)
	hostWidth := clamp(width*24/100, 16, 30)
	userWidth := clamp(width*14/100, 10, 18)
	tagsWidth := clamp(width-nameWidth-hostWidth-userWidth-20, 10, 28)
	m.table.SetColumns([]table.Column{{Title: "Host", Width: nameWidth}, {Title: "Hostname", Width: hostWidth}, {Title: "User", Width: userWidth}, {Title: "Tags", Width: tagsWidth}, {Title: "Last Login", Width: 14}})
	height := m.height - 9
	if height < 4 {
		height = 4
	}
	m.table.SetHeight(height)
}

func formatLastLogin(timestamp int64) string {
	if timestamp == 0 {
		return "-"
	}
	d := time.Since(time.Unix(timestamp, 0))
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	default:
		return time.Unix(timestamp, 0).Format("2006-01-02")
	}
}

func clamp(value, minimum, maximum int) int {
	if value < minimum {
		return minimum
	}
	if value > maximum {
		return maximum
	}
	return value
}
