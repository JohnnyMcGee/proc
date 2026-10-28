package main

import (
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

const (
	splitAt  = 110 // from this width the entry under the cursor shows beside the list
	headerAt = 12  // from this height the header shows
	boxWidth = 80  // the draft box's width when the terminal's is unknown
)

// View renders the header, then the list, the open entry, or both side by side, then the footer, fitted to the
// terminal. Everything in it that came from a service is untrusted, so control characters are dropped from each
// piece of text before it is styled and reaches the terminal.
func (m *model) View() string {
	var header []string
	if m.height == 0 || m.height >= headerAt {
		header = m.header()
	}
	footer := m.footer()
	room := m.height - len(header) - len(footer)
	var body []string
	switch {
	case m.open:
		body = m.entry(*m.current(), m.width)
		if room > 0 {
			m.scroll = min(m.scroll, max(len(body)-room, 0))
			body = body[m.scroll:min(len(body), m.scroll+room)]
		}
	case m.split():
		body = m.splitView(room)
	default:
		body = m.listPane(m.width, room, false)
	}
	for room > 0 && len(body) < room { // the footer sits at the bottom
		body = append(body, "")
	}
	return m.scan(strings.Join(slices.Concat(header, body, footer), "\n"))
}

func (m *model) split() bool { return m.width >= splitAt && !m.open && len(m.rows) > 0 }

// header is the brand, what waits on me, triage's inflow over the week, and the session's progress line.
func (m *model) header() []string {
	waiting, urgent := m.waiting()
	left := []seg{{" proc ", styleBrand}, {"  ", plain}}
	if waiting == 0 {
		left = append(left, seg{"all clear", styleDone})
	} else {
		left = append(left, seg{fmt.Sprintf("%d waiting", waiting), styleTitle})
	}
	if urgent > 0 {
		left = append(left, seg{" · ", styleDust}, seg{fmt.Sprintf("!%d urgent", urgent), styleUrgent})
	}
	if n := len(m.decided); n > 0 {
		left = append(left, seg{" · ", styleDust}, seg{fmt.Sprintf("%d decided", n), styleDone})
	}
	right := []seg{
		{"7d ", styleDust}, {sparkline(m.inflow), styleAction},
		{"  " + strings.ToLower(m.now().Local().Format("Mon 2 Jan 15:04")) + " ", styleDust},
	}
	return []string{spread(render(left), render(right), m.width), m.gaugeLine(), ""}
}

// gaugeLine fills as I decide this session's entries.
func (m *model) gaugeLine() string {
	width := m.width
	if width == 0 {
		width = 40
	}
	filled := min(max(int(math.Round(m.gauge.pos*float64(width))), 0), width)
	return styleDone.Render(strings.Repeat("━", filled)) + styleChalk.Render(strings.Repeat("━", width-filled))
}

func sparkline(counts []int) string {
	const levels = "▁▂▃▄▅▆▇█"
	top := slices.Max(append([]int{1}, counts...))
	var b strings.Builder
	for _, c := range counts {
		b.WriteRune([]rune(levels)[c*7/top])
	}
	return b.String()
}

// spread puts left at the start of the line and right at its end, dropping right when both do not fit.
func spread(left, right string, width int) string {
	gap := width - ansi.StringWidth(left) - ansi.StringWidth(right)
	switch {
	case width == 0:
		return left + "   " + right
	case gap < 1:
		return ansi.Truncate(left, width, "")
	}
	return left + strings.Repeat(" ", gap) + right
}

func (m *model) footer() []string {
	lines := append([]string{styleChalk.Render(strings.Repeat("─", m.width))}, m.wrap([]line{m.statusLine()}, m.width)...)
	return append(lines, strings.Split(m.keysLine(), "\n")...)
}

func (m *model) statusLine() line {
	switch {
	case m.prompt != nil:
		return text(seg{"✎ " + m.prompt.label + " › ", styleLamp}, seg{string(m.prompt.text), styleInk}, seg{"▏", styleLamp})
	case m.status != "":
		dot := lipgloss.NewStyle().Foreground(blend(dust, lamp, m.flash.pos))
		return text(seg{"◆ ", dot}, seg{m.status, styleInk})
	}
	return line{}
}

// keys are the footer's hints. A click on one presses its key.
func (m *model) keys() []hint {
	switch {
	case m.prompt != nil:
		return []hint{{"enter", "⏎", "save"}, {"esc", "esc", "cancel"}}
	case m.open:
		return []hint{{"a", "a", "approve"}, {"e", "e", "edit"}, {"s", "s", "skip"}, {"p", "p", "silence"}, {"", "j/k", "scroll"}, {"esc", "esc", "back"}, {"q", "q", "quit"}}
	}
	return []hint{{"a", "a", "approve"}, {"e", "e", "edit"}, {"s", "s", "skip"}, {"p", "p", "silence"}, {"enter", "⏎", "open"}, {"q", "q", "quit"}}
}

type hint struct{ key, cap, label string } // key is what a click presses; empty when the hint is not one key

func (m *model) keysLine() string {
	var parts []string
	for _, h := range m.keys() {
		part := styleLamp.Render(h.cap) + styleDust.Render(" "+h.label)
		if h.key != "" {
			part = m.mark("key:"+h.key, part)
		}
		parts = append(parts, part)
	}
	keys := strings.Join(parts, "   ")
	if m.width > 0 {
		keys = ansi.Wrap(keys, m.width, "")
	}
	return keys
}

// listPane is the queue grouped by project, scrolled to keep the cursor's row in room. A summary is cut to its
// column so each entry stays one row; the entry view shows it whole. Compact rows, beside the entry, mark where
// an entry stands without saying it.
func (m *model) listPane(width, room int, compact bool) []string {
	if len(m.rows) == 0 {
		return m.allClear(width)
	}
	var lines []string
	top, end := 0, 0
	for i, row := range m.rows {
		first := i == 0 || m.rows[i-1].Project != row.Project
		if first {
			if i > 0 {
				lines = append(lines, "")
			}
			lines = append(lines, m.wrap([]line{m.projectHeading(row.Project, width)}, width)...)
		}
		wrapped := m.wrap([]line{m.listRow(i, width, compact)}, width)
		if i == m.cursor { // its background runs to the edge
			for j, l := range wrapped {
				wrapped[j] = l + plain.Background(shade).Render(strings.Repeat(" ", max(width-ansi.StringWidth(l), 0)))
			}
		}
		wrapped[0] = m.mark(fmt.Sprintf("row:%d", i), wrapped[0])
		if i == m.cursor {
			top, end = len(lines), len(lines)+len(wrapped)
			if first {
				top--
			}
		}
		lines = append(lines, wrapped...)
	}
	if room <= 0 || len(lines) <= room {
		m.listTop = 0
		return lines
	}
	m.listTop = min(max(m.listTop, end-room), top, len(lines)-room) // scroll no further than the cursor needs
	return lines[m.listTop : m.listTop+room]
}

func (m *model) projectHeading(project string, width int) line {
	waiting := 0
	for _, row := range m.rows {
		if row.Project == project && row.Decision == nil && row.Status == "pending" {
			waiting++
		}
	}
	label, note := "◆ "+strings.ToUpper(project)+" ", seg{" all clear", styleDone}
	if waiting > 0 {
		note = seg{fmt.Sprintf(" %d waiting", waiting), styleDust}
	}
	fill := 8
	if width > 0 {
		fill = max(width-ansi.StringWidth(label)-ansi.StringWidth(note.text), 1)
	}
	return text(seg{label, styleProject}, seg{strings.Repeat("┄", fill), styleChalk}, note)
}

func (m *model) listRow(i, width int, compact bool) line {
	row := m.rows[i]
	cursor := i == m.cursor
	gutter, flag := seg{"  ", plain}, seg{"  ", plain}
	if cursor {
		gutter = seg{"▌ ", styleLamp}
	}
	if row.Urgency == "urgent" {
		flag = seg{"! ", styleUrgent}
	}
	waiting := row.Decision == nil && row.Status == "pending"
	summaryStyle := styleInk
	switch {
	case row.Decision != nil && row.Decision.Verdict == "skip":
		summaryStyle = styleDust.Strikethrough(true)
	case !waiting:
		summaryStyle = styleDust
	case cursor:
		summaryStyle = styleTitle
	}
	column := 60
	switch {
	case width > 0 && compact:
		column = min(max(width-21, 16), 60)
	case width > 0:
		column = min(max(width-40, 20), 60)
	}
	summary := ansi.Truncate(strings.ReplaceAll(row.Summary, "\n", " "), column, "…")
	summary += strings.Repeat(" ", max(column-ansi.StringWidth(summary), 0))
	verb := ansi.Truncate(verbOf(row.Action.Type), 12, "…")
	verb += strings.Repeat(" ", 12-ansi.StringWidth(verb))
	mark, words, style := verdict(row)
	stamp := seg{mark + " " + words, style}
	switch {
	case waiting:
		stamp = seg{}
	case compact:
		stamp = seg{mark, style}
	}
	body := []seg{{summary, summaryStyle}, {"  ", plain}, {verb, styleAction}, {" " + m.stampOffset(row.ID), plain}, stamp}
	l := line{lead: []seg{gutter, flag}, body: body}
	if cursor {
		l = l.on(shade)
	}
	return l
}

// stampOffset is how far the verdict just given has still to slide onto its row.
func (m *model) stampOffset(id string) string {
	if id != m.stamped {
		return ""
	}
	return strings.Repeat(" ", max(int(math.Round((1-m.stamp.pos)*12)), 0))
}

// verbOf is an action type without its service: gh_thread_reply is a thread reply.
func verbOf(action string) string {
	if action == "none" {
		return "—"
	}
	service, rest, ok := strings.Cut(action, "_")
	if ok && slices.Contains([]string{"gh", "github", "linear", "slack", "gmail", "calendar", "cal"}, service) {
		action = rest
	}
	return strings.ReplaceAll(action, "_", " ")
}

// allClear is the list when nothing is on it.
func (m *model) allClear(width int) []string {
	art := []line{
		text(seg{"   ·    ✦    ·   ", styleDust}),
		text(seg{"✦   ", styleLamp}, seg{"╭───────╮", styleDone}, seg{"   ·", styleDust}),
		text(seg{"    ", plain}, seg{"│   ✓   │", styleDone}, seg{"    ", plain}),
		text(seg{"·   ", styleDust}, seg{"╰───────╯", styleDone}, seg{"   ✦", styleLamp}),
		{},
		text(seg{"Nothing waits on you.", styleTitle}),
	}
	if next := nextSweep(m.now()); next != "" {
		art = append(art, text(seg{"next sweep " + next, styleDust}))
	}
	lines := []string{""}
	for _, l := range art {
		s := render(l.body)
		lines = append(lines, strings.Repeat(" ", max((width-ansi.StringWidth(s))/2, 2))+s)
	}
	return lines
}

// nextSweep is when the timers proc install wrote run the pipeline next, in local time.
func nextSweep(now time.Time) string {
	zone, err := time.LoadLocation(sweepZone)
	if err != nil {
		return ""
	}
	there := now.In(zone)
	for _, day := range []int{0, 1} {
		for _, at := range sweepTimes {
			t, err := time.ParseInLocation("15:04", at, zone)
			if err != nil {
				return ""
			}
			next := time.Date(there.Year(), there.Month(), there.Day()+day, t.Hour(), t.Minute(), 0, 0, zone)
			if next.After(now) {
				when := next.Local().Format("15:04")
				if day > 0 {
					when = "tomorrow " + when
				}
				return when
			}
		}
	}
	return ""
}

// splitView is the list beside the entry under the cursor, cut to room.
func (m *model) splitView(room int) []string {
	lw := max(48, m.width*9/20)
	rw := m.width - lw - 3
	left := m.listPane(lw, room, true)
	right := m.entry(*m.current(), rw)
	if room > 0 && len(right) > room {
		right = append(right[:room-1], styleDust.Render("⋯ ⏎ reads the rest"))
	}
	n := max(len(left), len(right))
	if room > 0 {
		n = room
	}
	sep := styleChalk.Render("│")
	lines := make([]string, n)
	for i := range lines {
		l, r := "", ""
		if i < len(left) {
			l = left[i]
		}
		if i < len(right) {
			r = right[i]
		}
		lines[i] = l + strings.Repeat(" ", max(lw-ansi.StringWidth(l), 0)) + " " + sep + " " + r
	}
	return lines
}

// entry is everything about one entry: where it stands, the draft the action will send, and the context of every
// item it merged.
func (m *model) entry(row Row, width int) []string {
	urgency := seg{row.Urgency, styleDust}
	if row.Urgency == "urgent" {
		urgency = seg{"! urgent", styleUrgent}
	}
	mark, words, style := verdict(row)
	lines := m.wrap([]line{
		text(urgency, seg{" · " + row.Kind + " · " + row.Project + " · ", styleDust}, seg{mark + " " + words, style}),
		text(seg{row.Summary, styleTitle}),
		text(seg{"↗ ", styleAction}, seg{row.Link, styleLink}),
		{},
	}, width)

	if draft := draftOf(row); draft != "" {
		title, border := row.Action.Type, styleChalk
		if row.Decision != nil && row.Decision.Draft != "" {
			title, border = title+" · ✎ edited", styleLamp
		}
		boxed := width
		if boxed == 0 {
			boxed = boxWidth
		}
		lines = append(lines, box(boxed, title, params(row.Action.Params), draft, border)...)
	} else {
		lines = append(lines, m.wrap([]line{text(seg{"⟶ ", styleLamp}, seg{row.Action.Type, styleAction}, seg{"  " + params(row.Action.Params), styleDust})}, width)...)
	}

	var context []line
	if m.itemsErr != nil {
		context = append(context, line{}, text(seg{"could not read the items: " + m.itemsErr.Error(), styleFailed}))
	}
	bar := []seg{{"┃ ", lipgloss.NewStyle().Foreground(plum)}}
	for _, item := range m.items {
		context = append(context, line{}, line{lead: bar, hang: bar, body: []seg{{item.Actor.Name, styleActor}, {"  " + item.Summary, styleTitle}}})
		for _, l := range strings.Split(strings.TrimRight(item.Context, "\n"), "\n") {
			context = append(context, line{lead: bar, hang: bar, body: []seg{{l, styleInk}}})
		}
	}
	return append(lines, m.wrap(context, width)...)
}

// box frames the draft: the action on its top edge, its parameters on the bottom.
func box(width int, title, note, body string, border lipgloss.Style) []string {
	inner := max(width-4, 1)
	title = ansi.Truncate(clean(title), max(width-6, 1), "…")
	top := border.Render("╭─ ") + styleAction.Bold(true).Render(title) + " " + border.Render(strings.Repeat("─", max(width-5-ansi.StringWidth(title), 0))+"╮")
	lines := []string{top}
	for _, para := range strings.Split(clean(strings.TrimRight(body, "\n")), "\n") {
		for _, part := range strings.Split(ansi.Wrap(para, inner, ""), "\n") {
			part = ansi.Truncate(part, inner, "")
			lines = append(lines, border.Render("│ ")+styleInk.Render(part)+strings.Repeat(" ", inner-ansi.StringWidth(part))+border.Render(" │"))
		}
	}
	note = clean(note)
	if fill := width - 5 - ansi.StringWidth(note); note != "" && fill > 0 {
		return append(lines, border.Render("╰"+strings.Repeat("─", fill)+" ")+styleDust.Render(note)+border.Render(" ─╯"))
	}
	return append(lines, border.Render("╰"+strings.Repeat("─", max(width-2, 0))+"╯"))
}

// draftOf is the text the action will send: my edit if I made one, otherwise triage's draft.
func draftOf(row Row) string {
	if row.Decision != nil && row.Decision.Draft != "" {
		return row.Decision.Draft
	}
	return row.Action.Draft
}

// verdict is where an entry stands, waiting on me, on the executor, or done, as a mark and the words for it.
func verdict(row Row) (mark, words string, style lipgloss.Style) {
	switch {
	case row.Result != nil && row.Result.Outcome == "success":
		return "✓", strings.TrimSpace("done " + row.Result.Link), styleDone
	case row.Result != nil:
		return "✗", "failed: " + row.Result.Error, styleFailed
	case row.Decision != nil && row.Decision.Verdict == "skip":
		return "↷", "skipped", styleDust
	case row.Decision != nil:
		s := "approved"
		if row.Decision.Draft != "" {
			s += ", edited"
		}
		if row.Decision.PromoteToSilent {
			s += ", silent from now on"
		}
		return "▸", s, styleDone
	case row.Status == "approved":
		return "▹", "approved (silent)", styleDone
	}
	return "○", "pending", stylePending
}

func params(p map[string]any) string {
	keys := make([]string, 0, len(p))
	for k := range p {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s %v", k, p[k]))
	}
	return strings.Join(parts, " · ")
}

// seg is a run of text in one style.
type seg struct {
	text  string
	style lipgloss.Style
}

// line is one line of the view. When it is wider than the terminal its body wraps, and the lines it wraps onto
// start with hang, or are indented to line up under the body, past the lead.
type line struct {
	lead, body, hang []seg
}

func text(segs ...seg) line { return line{body: segs} }

// on paints the line's background, the way the cursor's row stands out.
func (l line) on(bg lipgloss.TerminalColor) line {
	paint := func(segs []seg) []seg {
		out := make([]seg, len(segs))
		for i, s := range segs {
			out[i] = seg{s.text, s.style.Background(bg)}
		}
		return out
	}
	l.lead, l.body = paint(l.lead), paint(l.body)
	return l
}

func (m *model) wrap(lines []line, width int) []string {
	var out []string
	for _, l := range lines {
		lead, body := render(l.lead), render(l.body)
		hang := ansi.StringWidth(lead)
		cont := strings.Repeat(" ", hang)
		if l.hang != nil {
			cont = render(l.hang)
		}
		if width > hang {
			body = ansi.Wrap(body, width-hang, "")
		}
		for i, part := range strings.Split(body, "\n") {
			if i > 0 {
				lead = cont
			}
			if width > hang {
				part = ansi.Truncate(part, width-hang, "") // a wrap can leave the column padding past the edge
			}
			out = append(out, lead+part)
		}
	}
	return out
}

func render(segs []seg) string {
	var b strings.Builder
	for _, s := range segs {
		b.WriteString(s.style.Render(clean(s.text)))
	}
	return b.String()
}

func clean(s string) string { return strings.Map(printable, strings.ReplaceAll(s, "\t", "    ")) }

func printable(r rune) rune {
	if r == '\n' || !unicode.IsControl(r) {
		return r
	}
	return -1
}

// mark makes s a mouse target named id; scan finds the targets in the frame. Without a mouse they do nothing.
func (m *model) mark(id, s string) string {
	if m.zones == nil {
		return s
	}
	return m.zones.Mark(id, s)
}

func (m *model) scan(s string) string {
	if m.zones == nil {
		return s
	}
	return m.zones.Scan(s)
}
