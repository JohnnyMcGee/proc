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
	prompt *prompt
}

// prompt is the one-line reason field. Enter submits it (empty is fine), esc abandons the decision.
type prompt struct {
	label  string
	text   []rune
	submit func(reason string)
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
	if m.prompt != nil {
		m.typeKey(key)
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
	case "s":
		m.ask("skip, reason (optional)", func(reason string) {
			m.decide(Decision{Verdict: "skip", Reason: reason})
		})
	}
	return m, nil
}

func (m *model) ask(label string, submit func(string)) {
	if m.undecided() == nil {
		return
	}
	m.prompt = &prompt{label: label, submit: submit}
}

func (m *model) typeKey(key tea.KeyMsg) {
	p := m.prompt
	switch key.Type {
	case tea.KeyEnter:
		m.prompt = nil
		p.submit(strings.TrimSpace(string(p.text)))
	case tea.KeyEsc, tea.KeyCtrlC:
		m.prompt = nil
		m.status = "nothing recorded"
	case tea.KeyBackspace:
		if len(p.text) > 0 {
			p.text = p.text[:len(p.text)-1]
		}
	case tea.KeyRunes, tea.KeySpace:
		p.text = append(p.text, key.Runes...)
	}
}

// decide records a decision for the row under the cursor.
func (m *model) decide(d Decision) {
	row := m.undecided()
	if row == nil {
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

// undecided is the row under the cursor if it still waits on a decision. A decided entry is the executor's.
func (m *model) undecided() *Row {
	row := m.current()
	if row != nil && row.Decision != nil {
		m.status = "already decided"
		return nil
	}
	return row
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
	if m.prompt != nil {
		fmt.Fprintf(&b, "%s: %s_\n", m.prompt.label, string(m.prompt.text))
	} else {
		fmt.Fprintln(&b, m.status)
	}
	return b.String()
}
