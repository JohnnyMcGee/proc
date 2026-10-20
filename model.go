package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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

// edited is the editor exiting on the draft file of an entry.
type edited struct {
	entryID, path string
	err           error
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if msg, ok := msg.(edited); ok {
		m.afterEdit(msg)
		return m, nil
	}
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
	case "e":
		return m, m.edit()
	case "p":
		m.promote()
	}
	return m, nil
}

// promote approves the entry and asks the executor to make its action type silent in the profile from now on.
func (m *model) promote() {
	row := m.current()
	if row != nil && !promotable[row.Action.Type] {
		m.status = row.Action.Type + " always needs my approval"
		return
	}
	m.decide(Decision{Verdict: "approve", PromoteToSilent: true})
}

// edit opens the draft in $EDITOR. The file lives in the state directory, the only place proc writes.
func (m *model) edit() tea.Cmd {
	row := m.undecided()
	if row == nil {
		return nil
	}
	path := filepath.Join(m.root, "proc", row.ID+".md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		m.status = err.Error()
		return nil
	}
	if err := os.WriteFile(path, []byte(row.Action.Draft+"\n"), 0o644); err != nil {
		m.status = err.Error()
		return nil
	}
	id := row.ID
	return m.exec(editorCommand(path), func(err error) tea.Msg { return edited{id, path, err} })
}

func editorCommand(path string) *exec.Cmd {
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vi"
	}
	return exec.Command("sh", "-c", editor+` "$1"`, "editor", path) // $EDITOR may carry flags, e.g. "code --wait"
}

// afterEdit approves the entry with the edited draft, after an optional reason. An unchanged draft or a failed
// editor records nothing.
func (m *model) afterEdit(msg edited) {
	data, err := os.ReadFile(msg.path)
	os.Remove(msg.path)
	row := m.current()
	switch {
	case row == nil || row.ID != msg.entryID:
		return
	case msg.err != nil:
		m.status = "editor failed, nothing recorded: " + msg.err.Error()
		return
	case err != nil:
		m.status = err.Error()
		return
	}
	draft := strings.TrimRight(string(data), "\n")
	if draft == row.Action.Draft {
		m.status = "draft unchanged, nothing recorded"
		return
	}
	m.ask("approve edited draft, reason (optional)", func(reason string) {
		m.decide(Decision{Verdict: "approve", Draft: draft, Reason: reason})
	})
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
