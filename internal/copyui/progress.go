package copyui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/zzqjlcn/xsh/internal/filecopy"
)

type TransferFunc func(context.Context, filecopy.Reporter) error

type transferDone struct{ err error }

type ProgressModel struct {
	run        TransferFunc
	updates    chan tea.Msg
	context    context.Context
	cancel     context.CancelFunc
	progress   filecopy.Progress
	started    time.Time
	done       bool
	cancelling bool
	err        error
}

func NewProgress(run TransferFunc) ProgressModel {
	ctx, cancel := context.WithCancel(context.Background())
	return ProgressModel{
		run:     run,
		updates: make(chan tea.Msg, 32),
		context: ctx,
		cancel:  cancel,
		started: time.Now(),
	}
}

func (m ProgressModel) Init() tea.Cmd {
	go func() {
		err := m.run(m.context, func(progress filecopy.Progress) {
			select {
			case m.updates <- progress:
			case <-m.context.Done():
			}
		})
		m.updates <- transferDone{err: err}
	}()
	return waitForTransferUpdate(m.updates)
}

func (m ProgressModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch message := message.(type) {
	case filecopy.Progress:
		m.progress = message
		return m, waitForTransferUpdate(m.updates)
	case transferDone:
		m.done = true
		m.err = message.err
		return m, nil
	case tea.KeyMsg:
		if m.done {
			switch message.String() {
			case "enter", "esc", "q", "ctrl+c":
				return m, tea.Quit
			}
			return m, nil
		}
		switch message.String() {
		case "q", "ctrl+c", "esc":
			if !m.cancelling {
				m.cancelling = true
				m.cancel()
			}
		}
	}
	return m, nil
}

func (m ProgressModel) View() string {
	title := accentStyle.Render("xsh cp  Transfer")
	percent := 0.0
	if m.progress.TotalBytes > 0 {
		percent = float64(m.progress.BytesCopied) / float64(m.progress.TotalBytes)
	} else if m.done && m.err == nil {
		percent = 1
	}
	if percent > 1 {
		percent = 1
	}
	barWidth := 42
	filled := int(percent * float64(barWidth))
	bar := strings.Repeat("█", filled) + strings.Repeat("░", barWidth-filled)
	elapsed := time.Since(m.started).Seconds()
	speed := float64(0)
	if elapsed > 0 {
		speed = float64(m.progress.BytesCopied) / elapsed
	}
	phase := m.progress.Phase
	if phase == "" {
		phase = "Preparing"
	}
	if m.cancelling && !m.done {
		phase = "Cancelling"
	}
	status := fmt.Sprintf(
		"%s\n\n%s  %5.1f%%\n\nFiles: %d/%d\nData:  %s/%s\nSpeed: %s/s",
		phase,
		bar,
		percent*100,
		m.progress.FilesCopied,
		m.progress.TotalFiles,
		formatBytes(m.progress.BytesCopied),
		formatBytes(m.progress.TotalBytes),
		formatBytes(int64(speed)),
	)
	if m.progress.CurrentFile != "" {
		status += "\n\nCurrent: " + m.progress.CurrentFile
	}
	if m.done {
		if errors.Is(m.err, context.Canceled) {
			status += "\n\n" + warnStyle.Render("Copy cancelled")
		} else if m.err != nil {
			status += "\n\n" + warnStyle.Render("Failed: "+m.err.Error())
		} else {
			status += "\n\n" + accentStyle.Render("Copy complete")
		}
		status += "\n\n" + mutedStyle.Render("Press Enter to return")
	} else {
		status += "\n\n" + mutedStyle.Render("Esc/q cancel")
	}
	return title + "\n\n" + status
}

func (m ProgressModel) Result() error { return m.err }

func waitForTransferUpdate(updates <-chan tea.Msg) tea.Cmd {
	return func() tea.Msg { return <-updates }
}

func formatBytes(value int64) string {
	const unit = 1024
	if value < unit {
		return fmt.Sprintf("%d B", value)
	}
	divisor, exponent := int64(unit), 0
	for size := value / unit; size >= unit; size /= unit {
		divisor *= unit
		exponent++
	}
	return fmt.Sprintf("%.1f %ciB", float64(value)/float64(divisor), "KMGTPE"[exponent])
}
