package main

import (
	"fmt"
	"os/exec"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

type model struct {
	root   string
	now    func() time.Time
	exec   func(*exec.Cmd, tea.ExecCallback) tea.Cmd // tea.ExecProcess; tests run the editor synchronously
	rows   []Row
	cursor int
	status string // one line under the list: the last thing that happened, or why a key did nothing
}

func newModel(root string, now func() time.Time) (*model, error) {
	rows, err := loadRows(root)
	if err != nil {
		return nil, err
	}
	return &model{root: root, now: now, exec: tea.ExecProcess, rows: rows}, nil
}

func (m *model) Init() tea.Cmd { return nil }

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	switch key.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "j", "down":
		m.cursor = min(m.cursor+1, max(len(m.rows)-1, 0))
	case "k", "up":
		m.cursor = max(m.cursor-1, 0)
	case "a":
		m.decide(Decision{Verdict: "approve"})
	}
	return m, nil
}

// decide records a decision for the row under the cursor, once: a decided entry is the executor's.
func (m *model) decide(d Decision) {
	row := m.current()
	if row == nil {
		return
	}
	if row.Decision != nil {
		m.status = "already decided"
		return
	}
	d.EntryID, d.At = row.ID, stamp(m.now())
	if err := appendDecision(m.root, d); err != nil {
		m.status = "could not record the decision: " + err.Error()
		return
	}
	row.Decision = &d
	m.status = fmt.Sprintf("%s: %s", d.Verdict, row.Summary)
}

func (m *model) current() *Row {
	if m.cursor >= len(m.rows) {
		return nil
	}
	return &m.rows[m.cursor]
}

func (m *model) View() string {
	var b strings.Builder
	for i, row := range m.rows {
		pointer := "  "
		if i == m.cursor {
			pointer = "> "
		}
		fmt.Fprintf(&b, "%s%s %s\n", pointer, row.Project, row.Summary)
	}
	b.WriteString(m.status + "\n")
	return b.String()
}
