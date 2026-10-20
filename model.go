package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

type model struct {
	root        string
	now         func() time.Time
	exec        func(*exec.Cmd, tea.ExecCallback) tea.Cmd // tea.ExecProcess; tests run the editor synchronously
	rows        []Row
	cursor      int
	status      string // one line under the list: the last thing that happened, or why a key did nothing
	prompt      *prompt
	open        bool // showing the entry under the cursor instead of the list
	scroll      int  // first line of the entry view on screen
	height      int
	decided     bool   // a decision was recorded this session
	executorLog string // where the executor started on quit writes; empty when it did not start
	executorErr error
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
	switch msg := msg.(type) {
	case edited:
		m.afterEdit(msg)
		return m, nil
	case tea.WindowSizeMsg:
		m.height = msg.Height
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
		m.quit()
		return m, tea.Quit
	case "j", "down":
		if m.open {
			m.scroll++
		} else {
			m.cursor = min(m.cursor+1, max(len(m.rows)-1, 0))
		}
	case "k", "up":
		if m.open {
			m.scroll = max(m.scroll-1, 0)
		} else {
			m.cursor = max(m.cursor-1, 0)
		}
	case "enter":
		m.open, m.scroll = m.current() != nil, 0
	case "esc":
		m.open = false
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

// quit starts the executor when it has work: decisions from this session (a skip's reason is a lesson to learn),
// or an approved entry still without a result.
func (m *model) quit() {
	work := m.decided
	for _, row := range m.rows {
		approved := row.Status == "approved" || row.Decision != nil && row.Decision.Verdict == "approve"
		work = work || approved && row.Result == nil
	}
	if !work {
		return
	}
	m.executorLog, m.executorErr = startExecutor(m.root)
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
	m.decided = true
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
	var lines []string
	if m.open {
		lines = m.entryView(*m.current())
	} else {
		lines = m.listView()
	}
	footer := []string{"", m.status, "a approve · e edit · s skip · p approve and make silent · enter open · esc back · q quit"}
	if m.prompt != nil {
		footer[1] = fmt.Sprintf("%s: %s_", m.prompt.label, string(m.prompt.text))
	}
	if m.open && m.height > len(footer) {
		m.scroll = min(m.scroll, max(len(lines)-(m.height-len(footer)), 0))
		lines = lines[m.scroll:min(len(lines), m.scroll+m.height-len(footer))]
	}
	return strings.Join(append(lines, footer...), "\n")
}

func (m *model) listView() []string {
	if len(m.rows) == 0 {
		return []string{"Nothing waits on you."}
	}
	var lines []string
	project := ""
	for i, row := range m.rows {
		if row.Project != project {
			project = row.Project
			lines = append(lines, project)
		}
		pointer, urgent := "  ", " "
		if i == m.cursor {
			pointer = "> "
		}
		if row.Urgency == "urgent" {
			urgent = "!"
		}
		lines = append(lines, fmt.Sprintf("%s%s %-60s  %-16s %s", pointer, urgent, row.Summary, row.Action.Type, state(row)))
	}
	return lines
}

func (m *model) entryView(row Row) []string {
	lines := []string{
		row.Summary,
		row.Link,
		fmt.Sprintf("%s · %s · %s · %s", row.Project, row.Urgency, row.Kind, state(row)),
		"",
		"Action: " + row.Action.Type + params(row.Action.Params),
	}
	draft := row.Action.Draft
	if row.Decision != nil && row.Decision.Draft != "" {
		draft = row.Decision.Draft
	}
	if draft != "" {
		lines = append(append(lines, "", "Draft:"), indent(draft)...)
	}
	items, err := loadItems(m.root, row.Project, row.ItemIDs)
	if err != nil {
		lines = append(lines, "", "could not read the items: "+err.Error())
	}
	for _, item := range items {
		lines = append(lines, "", fmt.Sprintf("From %s: %s", item.Actor.Name, item.Summary))
		lines = append(lines, indent(item.Context)...)
	}
	return lines
}

// state is where an entry stands: waiting on me, on the executor, or done.
func state(row Row) string {
	switch {
	case row.Result != nil && row.Result.Outcome == "success":
		return strings.TrimSpace("✓ done " + row.Result.Link)
	case row.Result != nil:
		return "✗ failed: " + row.Result.Error
	case row.Decision != nil && row.Decision.Verdict == "skip":
		return "skipped"
	case row.Decision != nil:
		s := "approved"
		if row.Decision.Draft != "" {
			s += ", edited"
		}
		if row.Decision.PromoteToSilent {
			s += ", silent from now on"
		}
		return s
	case row.Status == "approved":
		return "approved (silent)"
	}
	return "pending"
}

func params(p map[string]any) string {
	if len(p) == 0 {
		return ""
	}
	keys := make([]string, 0, len(p))
	for k := range p {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%v", k, p[k]))
	}
	return " (" + strings.Join(parts, ", ") + ")"
}

func indent(text string) []string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	for i, line := range lines {
		lines[i] = "    " + line
	}
	return lines
}
