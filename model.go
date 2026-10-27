package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
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
	items       []Item
	itemsErr    error // the open entry's items, read when it opened
	width       int
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
	cancel string // the status after esc
}

func newModel(root string, now func() time.Time) (*model, error) {
	rows, err := loadRows(root, now())
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
		m.width, m.height = msg.Width, msg.Height
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
	case "q":
		m.quit()
		return m, tea.Quit
	case "ctrl+c": // abort: decisions are saved, and the next q starts the executor on them
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
		if row := m.current(); row != nil {
			m.open, m.scroll = true, 0
			m.items, m.itemsErr = loadItems(m.root, row.Project, row.ItemIDs)
		}
	case "esc":
		m.open = false
	case "a":
		m.decide(Decision{Verdict: "approve"})
	case "s":
		m.ask("skip, reason (optional)", "nothing recorded", func(reason string) {
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
	row := m.undecided()
	if row == nil {
		return
	}
	if !promotable[row.Action.Type] {
		m.status = row.Action.Type + " cannot be made silent"
		return
	}
	m.decide(Decision{Verdict: "approve", PromoteToSilent: true})
}

// edit opens the draft in $EDITOR. The file lives in the state directory, the only place proc writes, and stays
// there until the edit is recorded or dropped, so an edit left at the reason prompt reopens as I left it.
func (m *model) edit() tea.Cmd {
	row := m.undecided()
	if row == nil {
		return nil
	}
	path := m.draftPath(row.ID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		m.status = err.Error()
		return nil
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		if err := os.WriteFile(path, []byte(row.Action.Draft+"\n"), 0o644); err != nil {
			m.status = err.Error()
			return nil
		}
	}
	id := row.ID
	return m.exec(editorCommand(path), func(err error) tea.Msg { return edited{id, path, err} })
}

func (m *model) draftPath(id string) string {
	return filepath.Join(m.root, "proc", id+".md")
}

func editorCommand(path string) *exec.Cmd {
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vi"
	}
	return exec.Command("sh", "-c", editor+` "$1"`, "editor", path) // $EDITOR may carry flags, e.g. "code --wait"
}

// afterEdit approves the entry with the edited draft, after an optional reason. A failed editor, or a draft left
// unchanged or emptied, records nothing.
func (m *model) afterEdit(msg edited) {
	data, err := os.ReadFile(msg.path)
	draft := strings.TrimRight(string(data), "\n")
	row := m.current()
	switch {
	case row == nil || row.ID != msg.entryID:
		return
	case msg.err != nil:
		m.status = "editor failed, nothing recorded: " + msg.err.Error()
	case err != nil:
		m.status = err.Error()
	case draft == strings.TrimRight(row.Action.Draft, "\n"):
		m.status = "draft unchanged, nothing recorded"
	case strings.TrimSpace(draft) == "":
		m.status = "draft emptied, nothing recorded; s skips the entry"
	default:
		m.ask("approve edited draft, reason (optional)", "nothing recorded; e reopens your edit", func(reason string) {
			m.decide(Decision{Verdict: "approve", Draft: draft, Reason: reason})
		})
		return
	}
	os.Remove(msg.path)
}

func (m *model) ask(label, cancel string, submit func(string)) {
	if m.undecided() == nil {
		return
	}
	m.prompt = &prompt{label: label, submit: submit, cancel: cancel}
}

func (m *model) typeKey(key tea.KeyMsg) {
	p := m.prompt
	switch key.Type {
	case tea.KeyEnter:
		m.prompt = nil
		p.submit(strings.TrimSpace(string(p.text)))
	case tea.KeyEsc, tea.KeyCtrlC:
		m.prompt = nil
		m.status = p.cancel
	case tea.KeyBackspace:
		if len(p.text) > 0 {
			p.text = p.text[:len(p.text)-1]
		}
	case tea.KeyRunes, tea.KeySpace:
		for _, r := range key.Runes {
			if r == '\n' || r == '\r' { // a pasted reason stays one line
				r = ' '
			}
			p.text = append(p.text, r)
		}
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
	os.Remove(m.draftPath(row.ID)) // an edit left at the reason prompt is moot now
	m.status = fmt.Sprintf("%s: %s", d.Verdict, row.Summary)
}

// undecided is the row under the cursor if it still waits on a decision. A decided entry, or one triage approved
// silently, is the executor's.
func (m *model) undecided() *Row {
	row := m.current()
	switch {
	case row != nil && row.Decision != nil:
		m.status = "already decided"
		return nil
	case row != nil && row.Status == "approved":
		m.status = "already approved silently, waiting on the executor"
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

// View renders the list or the open entry, wrapped to the terminal's width and cut to its height. Everything in it
// that came from a service is untrusted, so control characters are dropped from each piece of text before it is
// styled and reaches the terminal.
func (m *model) View() string {
	status := line{body: []seg{{m.status, styleStatus}}}
	if m.prompt != nil {
		status = line{body: []seg{{m.prompt.label + ": ", stylePrompt}, {string(m.prompt.text) + "_", plain}}}
	}
	footer := m.wrap([]line{{}, status, help()})
	room := m.height - len(footer)
	var lines []string
	if m.open {
		lines = m.wrap(m.entryView(*m.current()))
		if room > 0 {
			m.scroll = min(m.scroll, max(len(lines)-room, 0))
		}
	} else {
		list, cursorLine := m.listView()
		for i, l := range list {
			lines = append(lines, m.wrap([]line{l})...)
			if i == cursorLine && room > 0 {
				m.scroll = max(len(lines)-room, 0)
			}
		}
	}
	if room > 0 {
		lines = lines[m.scroll:min(len(lines), m.scroll+room)]
	}
	return strings.Join(append(lines, footer...), "\n")
}

var (
	plain         = lipgloss.NewStyle()
	faint         = lipgloss.NewStyle().Faint(true)
	bold          = lipgloss.NewStyle().Bold(true)
	styleProject  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("5"))
	styleCursor   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6"))
	styleUrgent   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("1"))
	styleAction   = lipgloss.NewStyle().Foreground(lipgloss.Color("4"))
	styleLink     = lipgloss.NewStyle().Underline(true).Foreground(lipgloss.Color("4"))
	styleActor    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6"))
	styleStatus   = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	stylePrompt   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6"))
	stylePending  = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	styleDone     = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	styleFailed   = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	styleApproved = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
)

// seg is a run of text in one style.
type seg struct {
	text  string
	style lipgloss.Style
}

// line is one line of the view. When it is wider than the terminal its body wraps, and the lines it wraps onto are
// indented to line up under the body, past the lead.
type line struct {
	lead, body []seg
}

func text(segs ...seg) line { return line{body: segs} }

func (m *model) wrap(lines []line) []string {
	var out []string
	for _, l := range lines {
		lead, body := render(l.lead), render(l.body)
		hang := ansi.StringWidth(lead)
		if m.width > hang {
			body = ansi.Wrap(body, m.width-hang, "")
		}
		for i, part := range strings.Split(body, "\n") {
			if i > 0 {
				lead = strings.Repeat(" ", hang)
			}
			if m.width > hang {
				part = ansi.Truncate(part, m.width-hang, "") // a wrap can leave the column padding past the edge
			}
			out = append(out, lead+part)
		}
	}
	return out
}

func render(segs []seg) string {
	var b strings.Builder
	for _, s := range segs {
		b.WriteString(s.style.Render(strings.Map(printable, strings.ReplaceAll(s.text, "\t", "    "))))
	}
	return b.String()
}

func printable(r rune) rune {
	if r == '\n' || !unicode.IsControl(r) {
		return r
	}
	return -1
}

func help() line {
	keys := []string{"a", "approve", "e", "edit", "s", "skip", "p", "approve and make silent", "enter", "open", "esc", "back", "q", "quit"}
	var segs []seg
	for i := 0; i < len(keys); i += 2 {
		if i > 0 {
			segs = append(segs, seg{" · ", faint})
		}
		segs = append(segs, seg{keys[i], bold}, seg{" " + keys[i+1], faint})
	}
	return text(segs...)
}

// listView is the queue grouped by project, and the line the cursor is on. A summary is cut to its column so each
// entry stays one row; enter shows it whole.
func (m *model) listView() ([]line, int) {
	if len(m.rows) == 0 {
		return []line{text(seg{"Nothing waits on you.", faint})}, 0
	}
	column := 60
	if m.width > 0 {
		column = min(column, max(m.width-40, 20))
	}
	var lines []line
	cursorLine := 0
	project := ""
	for i, row := range m.rows {
		if row.Project != project {
			project = row.Project
			lines = append(lines, text(seg{project, styleProject}))
		}
		pointer, urgent, summaryStyle := seg{"  ", plain}, seg{"  ", plain}, plain
		if i == m.cursor {
			pointer, cursorLine, summaryStyle = seg{"> ", styleCursor}, len(lines), bold
		}
		if row.Urgency == "urgent" {
			urgent = seg{"! ", styleUrgent}
		}
		summary := ansi.Truncate(strings.ReplaceAll(row.Summary, "\n", " "), column, "…")
		summary += strings.Repeat(" ", column-ansi.StringWidth(summary))
		lines = append(lines, line{lead: []seg{pointer, urgent}, body: []seg{
			{summary, summaryStyle}, {"  ", plain}, {fmt.Sprintf("%-16s", row.Action.Type), styleAction}, {" ", plain}, state(row),
		}})
	}
	return lines, cursorLine
}

func (m *model) entryView(row Row) []line {
	lines := []line{
		text(seg{row.Summary, bold}),
		text(seg{row.Link, styleLink}),
		text(seg{fmt.Sprintf("%s · %s · %s · ", row.Project, row.Urgency, row.Kind), faint}, state(row)),
		{},
		text(seg{"Action: ", bold}, seg{row.Action.Type, styleAction}, seg{params(row.Action.Params), faint}),
	}
	draft := row.Action.Draft
	if row.Decision != nil && row.Decision.Draft != "" {
		draft = row.Decision.Draft
	}
	if draft != "" {
		lines = append(append(lines, line{}, text(seg{"Draft:", bold})), indent(draft)...)
	}
	if m.itemsErr != nil {
		lines = append(lines, line{}, text(seg{"could not read the items: " + m.itemsErr.Error(), styleFailed}))
	}
	for _, item := range m.items {
		lines = append(lines, line{}, text(seg{"From ", bold}, seg{item.Actor.Name, styleActor}, seg{": " + item.Summary, bold}))
		lines = append(lines, indent(item.Context)...)
	}
	return lines
}

// state is where an entry stands: waiting on me, on the executor, or done.
func state(row Row) seg {
	switch {
	case row.Result != nil && row.Result.Outcome == "success":
		return seg{strings.TrimSpace("✓ done " + row.Result.Link), styleDone}
	case row.Result != nil:
		return seg{"✗ failed: " + row.Result.Error, styleFailed}
	case row.Decision != nil && row.Decision.Verdict == "skip":
		return seg{"skipped", faint}
	case row.Decision != nil:
		s := "approved"
		if row.Decision.Draft != "" {
			s += ", edited"
		}
		if row.Decision.PromoteToSilent {
			s += ", silent from now on"
		}
		return seg{s, styleApproved}
	case row.Status == "approved":
		return seg{"approved (silent)", styleApproved}
	}
	return seg{"pending", stylePending}
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

func indent(text string) []line {
	var lines []line
	for _, l := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		lines = append(lines, line{lead: []seg{{"    ", plain}}, body: []seg{{l, plain}}})
	}
	return lines
}
