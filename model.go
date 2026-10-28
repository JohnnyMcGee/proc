package main

import (
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	zone "github.com/lrstanley/bubblezone"
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
	listTop     int  // first line of the list on screen
	items       []Item
	itemsErr    error // the items of the entry under the cursor, read when it got there
	width       int
	height      int
	decided     map[string]bool // entries decided this session: they can be decided again until proc quits
	executorLog string          // where the executor started on quit writes; empty when it did not start
	executorErr error
	inflow      []int         // entries triage queued on each of the last seven days, oldest first
	zones       *zone.Manager // mouse targets; nil without a mouse
	motion      bool          // animate; without it every spring is at rest, as in tests
	animating   bool          // a frame is on its way
	gauge       motion        // the header's progress line, as a fraction of this session's queue
	stamp       motion        // the verdict just given, sliding onto its row
	stamped     string        // the entry the stamp is for
	flash       motion        // the status dot's glow
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
	entries, err := loadQueue(root)
	if err != nil {
		return nil, err
	}
	m := &model{root: root, now: now, exec: tea.ExecProcess, rows: rows, decided: map[string]bool{}, inflow: inflow(entries, now())}
	m.gauge = motion{spring: gaugeSpring, pos: m.progress(), target: m.progress()}
	m.stamp = motion{spring: stampSpring, pos: 1, target: 1}
	m.flash = motion{spring: flashSpring}
	m.load()
	return m, nil
}

// inflow counts the entries triage queued on each of the seven days up to today.
func inflow(entries []Entry, now time.Time) []int {
	counts := make([]int, 7)
	y, mo, d := now.Local().Date()
	today := time.Date(y, mo, d, 0, 0, 0, 0, time.Local)
	for _, e := range entries {
		created, err := time.Parse(time.RFC3339, e.Created)
		if err != nil {
			continue
		}
		y, mo, d := created.Local().Date()
		ago := int(math.Round(today.Sub(time.Date(y, mo, d, 0, 0, 0, 0, time.Local)).Hours() / 24)) // a DST day is 23 or 25 hours
		if ago >= 0 && ago < len(counts) {
			counts[len(counts)-1-ago]++
		}
	}
	return counts
}

// waiting counts the entries waiting on my decision, and the urgent ones among them.
func (m *model) waiting() (waiting, urgent int) {
	for _, row := range m.rows {
		if row.Decision == nil && row.Status == "pending" {
			waiting++
			if row.Urgency == "urgent" {
				urgent++
			}
		}
	}
	return waiting, urgent
}

// progress is how much of this session's queue I have decided.
func (m *model) progress() float64 {
	waiting, _ := m.waiting()
	if waiting+len(m.decided) == 0 {
		return 1
	}
	return float64(len(m.decided)) / float64(waiting+len(m.decided))
}

// load reads the items of the entry under the cursor, for its context.
func (m *model) load() {
	if row := m.current(); row != nil {
		m.items, m.itemsErr = loadItems(m.root, row.Project, row.ItemIDs)
	}
}

func (m *model) Init() tea.Cmd { return nil }

// edited is the editor exiting on the draft file of an entry.
type edited struct {
	entryID, path string
	err           error
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if _, ok := msg.(frame); ok {
		return m, m.stepFrame()
	}
	status := m.status
	cmd := m.handle(msg)
	if m.status != status {
		m.flash.from(1, 0)
	}
	return m, tea.Batch(cmd, m.animate())
}

func (m *model) handle(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case edited:
		m.afterEdit(msg)
		return nil
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return nil
	case tea.MouseMsg:
		return m.click(msg)
	}
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return nil
	}
	if m.prompt != nil {
		m.typeKey(key)
		return nil
	}
	switch key.String() {
	case "q":
		m.quit()
		return tea.Quit
	case "ctrl+c": // abort: decisions are saved, and the next q starts the executor on them
		return tea.Quit
	case "j", "down":
		if m.open {
			m.scroll++
		} else {
			m.cursor = min(m.cursor+1, max(len(m.rows)-1, 0))
			m.load()
		}
	case "k", "up":
		if m.open {
			m.scroll = max(m.scroll-1, 0)
		} else {
			m.cursor = max(m.cursor-1, 0)
			m.load()
		}
	case "enter":
		if m.current() != nil {
			m.open, m.scroll = true, 0
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
		return m.edit()
	case "p":
		m.promote()
	}
	return nil
}

// click turns the mouse into keys: the wheel moves, a click on a row selects it or opens it when it is selected
// already, and a click on a key in the footer presses it.
func (m *model) click(msg tea.MouseMsg) tea.Cmd {
	switch {
	case m.prompt == nil && msg.Button == tea.MouseButtonWheelUp:
		return m.press("k")
	case m.prompt == nil && msg.Button == tea.MouseButtonWheelDown:
		return m.press("j")
	case m.zones == nil || msg.Button != tea.MouseButtonLeft || msg.Action != tea.MouseActionRelease:
		return nil
	}
	for _, h := range m.keys() {
		if h.key != "" && m.zones.Get("key:"+h.key).InBounds(msg) {
			return m.press(h.key)
		}
	}
	if m.open || m.prompt != nil {
		return nil
	}
	for i := range m.rows {
		if m.zones.Get(fmt.Sprintf("row:%d", i)).InBounds(msg) {
			if i == m.cursor {
				return m.press("enter")
			}
			m.cursor = i
			m.load()
			return nil
		}
	}
	return nil
}

func (m *model) press(key string) tea.Cmd {
	switch key {
	case "enter":
		return m.handle(tea.KeyMsg{Type: tea.KeyEnter})
	case "esc":
		return m.handle(tea.KeyMsg{Type: tea.KeyEsc})
	}
	return m.handle(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
}

// quit starts the executor when it has work: decisions from this session (a skip's reason is a lesson to learn),
// or an approved entry still without a result.
func (m *model) quit() {
	work := len(m.decided) > 0
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

// edit opens the draft in $EDITOR, my earlier edit if I made one this session. The file lives in the state directory, the only place proc writes, and stays
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
		if err := os.WriteFile(path, []byte(draftOf(*row)+"\n"), 0o644); err != nil {
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
	case draft == strings.TrimRight(draftOf(*row), "\n"):
		m.status = "draft unchanged, nothing recorded"
	case strings.TrimSpace(draft) == "":
		m.status = "draft emptied, nothing recorded; s skips the entry"
	default:
		if draft == strings.TrimRight(row.Action.Draft, "\n") { // edited back to triage's draft
			draft = ""
		}
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

// decide records a decision for the row under the cursor, or changes the one made for it this session.
func (m *model) decide(d Decision) {
	row := m.undecided()
	if row == nil {
		return
	}
	d.EntryID, d.At = row.ID, stamp(m.now())
	record, verb := appendDecision, ""
	if row.Decision != nil {
		record, verb = replaceDecision, "changed to "
	}
	if err := record(m.root, d); err != nil {
		m.status = "could not record the decision: " + err.Error()
		return
	}
	row.Decision = &d
	m.decided[row.ID] = true
	os.Remove(m.draftPath(row.ID)) // an edit left at the reason prompt is moot now
	m.status = fmt.Sprintf("%s%s: %s", verb, d.Verdict, row.Summary)
	if waiting, _ := m.waiting(); waiting == 0 {
		m.status = fmt.Sprintf("✦ all clear · q hands %d decisions to the executor", len(m.decided))
	}
	m.stamped = row.ID
	m.stamp.from(0, 1)
}

// undecided is the row under the cursor if it can still be decided: it waits on a decision, or I decided it this
// session and the executor has not acted on it yet. A decision from an earlier session, or an entry triage approved
// silently, is the executor's.
func (m *model) undecided() *Row {
	row := m.current()
	switch {
	case row == nil:
		return nil
	case row.Decision != nil && !m.decided[row.ID]:
		m.status = "already decided"
		return nil
	case row.Status == "approved" && row.Decision == nil:
		m.status = "already approved silently, waiting on the executor"
		return nil
	case row.Decision != nil:
		result, err := resultFor(m.root, row.ID)
		if err != nil {
			m.status = err.Error()
			return nil
		}
		if result != nil {
			row.Result = result
			m.status = "the executor already acted on it"
			return nil
		}
	}
	return row
}

func (m *model) current() *Row {
	if m.cursor >= len(m.rows) {
		return nil
	}
	return &m.rows[m.cursor]
}
