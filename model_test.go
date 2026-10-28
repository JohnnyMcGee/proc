package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
)

var now = time.Date(2026, 10, 20, 9, 30, 0, 0, time.UTC)

// fixtureState copies testdata/state into a scratch XDG_STATE_HOME and returns the process state directory.
func fixtureState(t *testing.T) string {
	t.Helper()
	return fixtureStateFrom(t, "testdata/state")
}

// fixtureStateFrom copies a fixture XDG_STATE_HOME into a scratch one and returns the process state directory.
func fixtureStateFrom(t *testing.T, dir string) string {
	t.Helper()
	home := t.TempDir()
	if err := os.CopyFS(home, os.DirFS(dir)); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(home, "process")
}

func start(t *testing.T, root string) tea.Model {
	t.Helper()
	m, err := newModel(root, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	m.exec = func(c *exec.Cmd, done tea.ExecCallback) tea.Cmd {
		return func() tea.Msg { return done(c.Run()) }
	}
	return m
}

// press sends each key to the model and runs the commands it returns, feeding their messages back, the way the
// bubbletea runtime would. Named keys are "enter", "esc" and "backspace"; anything else is typed one key at a time.
func press(t *testing.T, m tea.Model, keys ...string) tea.Model {
	t.Helper()
	for _, key := range keys {
		switch key {
		case "enter":
			m = deliver(m, tea.KeyMsg{Type: tea.KeyEnter})
		case "esc":
			m = deliver(m, tea.KeyMsg{Type: tea.KeyEsc})
		case "backspace":
			m = deliver(m, tea.KeyMsg{Type: tea.KeyBackspace})
		case "ctrl+c":
			m = deliver(m, tea.KeyMsg{Type: tea.KeyCtrlC})
		default:
			for _, r := range key {
				if r == ' ' {
					m = deliver(m, tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{r}})
				} else {
					m = deliver(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
				}
			}
		}
	}
	return m
}

func deliver(m tea.Model, msg tea.Msg) tea.Model {
	m, cmd := m.Update(msg)
	for _, next := range drain(cmd) {
		m = deliver(m, next)
	}
	return m
}

func drain(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	switch msg := cmd().(type) {
	case nil, tea.QuitMsg:
		return nil
	case tea.BatchMsg:
		var msgs []tea.Msg
		for _, c := range msg {
			msgs = append(msgs, drain(c)...)
		}
		return msgs
	default:
		return []tea.Msg{msg}
	}
}

func decisions(t *testing.T, root string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "decisions.jsonl"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	for _, line := range splitLines(data) {
		var d map[string]any
		if err := json.Unmarshal(line, &d); err != nil {
			t.Fatalf("decisions.jsonl: %v: %s", err, line)
		}
		if err := validate("decision", d); err != nil {
			t.Fatalf("decision does not match the shared schema: %v", err)
		}
		out = append(out, d)
	}
	return out
}

func only(t *testing.T, root string) map[string]any {
	t.Helper()
	ds := decisions(t, root)
	if len(ds) != 1 {
		t.Fatalf("want exactly one decision, got %d: %v", len(ds), ds)
	}
	return ds[0]
}

func TestApproveWritesADecisionWithOneKey(t *testing.T) {
	root := fixtureState(t)
	press(t, start(t, root), "a")

	d := only(t, root)
	want := map[string]any{"entry_id": "q-aaaaaaaaaaa2", "verdict": "approve", "at": "2026-10-20T09:30:00Z"}
	assertDecision(t, d, want)
}

func assertDecision(t *testing.T, got, want map[string]any) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("decision %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("decision %s = %v, want %v (decision %v)", k, got[k], v, got)
		}
	}
}

func TestSkipRecordsTheReasonITyped(t *testing.T) {
	root := fixtureState(t)
	press(t, start(t, root), "s", "not mine, Lee owns cachx", "backspace", "e", "enter")

	assertDecision(t, only(t, root), map[string]any{
		"entry_id": "q-aaaaaaaaaaa2", "verdict": "skip", "reason": "not mine, Lee owns cache", "at": "2026-10-20T09:30:00Z",
	})
}

func TestSkipWithoutAReasonRecordsNone(t *testing.T) {
	root := fixtureState(t)
	press(t, start(t, root), "s", "enter")

	assertDecision(t, only(t, root), map[string]any{"entry_id": "q-aaaaaaaaaaa2", "verdict": "skip", "at": "2026-10-20T09:30:00Z"})
}

func TestEscapeFromTheReasonPromptRecordsNothing(t *testing.T) {
	root := fixtureState(t)
	press(t, start(t, root), "s", "nope", "esc")

	if ds := decisions(t, root); len(ds) != 0 {
		t.Fatalf("want no decision, got %v", ds)
	}
}

// editor points $EDITOR at a script that rewrites the draft file the way I would by hand.
func editor(t *testing.T, script string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "editor")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EDITOR", path)
}

func TestEditedDraftRoundTripsIntoTheDecision(t *testing.T) {
	root := fixtureState(t)
	editor(t, `sed 's/per request/per request on purpose/' "$1" > "$1.new" && mv "$1.new" "$1"`)
	press(t, start(t, root), "e", "too terse", "enter")

	assertDecision(t, only(t, root), map[string]any{
		"entry_id": "q-aaaaaaaaaaa2", "verdict": "approve",
		"draft":  "It is per request on purpose so a stale entry cannot outlive a deploy.",
		"reason": "too terse", "at": "2026-10-20T09:30:00Z",
	})
}

func TestLeavingTheDraftUnchangedRecordsNothing(t *testing.T) {
	root := fixtureState(t)
	editor(t, `true`)
	press(t, start(t, root), "e", "enter")

	if ds := decisions(t, root); len(ds) != 0 {
		t.Fatalf("want no decision, got %v", ds)
	}
}

func TestAFailedEditorRecordsNothing(t *testing.T) {
	root := fixtureState(t)
	editor(t, `echo changed > "$1"; exit 1`)
	press(t, start(t, root), "e", "enter")

	if ds := decisions(t, root); len(ds) != 0 {
		t.Fatalf("want no decision, got %v", ds)
	}
}

func TestPromoteToSilentApprovesAndAsksTheExecutorToStopAsking(t *testing.T) {
	root := fixtureState(t)
	press(t, start(t, root), "p")

	assertDecision(t, only(t, root), map[string]any{
		"entry_id": "q-aaaaaaaaaaa2", "verdict": "approve", "promote_to_silent": true, "at": "2026-10-20T09:30:00Z",
	})
}

func TestSubmittingAReviewCannotBePromotedToSilent(t *testing.T) {
	root := fixtureState(t)
	press(t, start(t, root), "j", "p")

	if ds := decisions(t, root); len(ds) != 0 {
		t.Fatalf("want no decision, got %v", ds)
	}
}

func TestQueueIsListedByProjectThenUrgencyWithoutFYIOrSupersededEntries(t *testing.T) {
	root := fixtureState(t)
	press(t, start(t, root), "a", "j", "s", "enter", "j", "j", "j", "j", "a")

	ds := decisions(t, root)
	var got []string
	for _, d := range ds {
		got = append(got, d["entry_id"].(string))
	}
	want := []string{"q-aaaaaaaaaaa2", "q-aaaaaaaaaaa1", "q-bbbbbbbbbbb1"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("decided %v, want %v", got, want)
	}
}

func TestADecisionFromAnEarlierSessionCannotBeChanged(t *testing.T) {
	root := fixtureState(t)
	press(t, start(t, root), "a")
	press(t, start(t, root), "s", "enter", "p")

	assertDecision(t, only(t, root), map[string]any{"entry_id": "q-aaaaaaaaaaa2", "verdict": "approve", "at": "2026-10-20T09:30:00Z"})
}

func TestIChangeMyMindAsOftenAsIWantWhileProcIsOpen(t *testing.T) {
	root := fixtureState(t)
	m := press(t, start(t, root), "a", "s", "not mine", "enter", "p", "s", "Lee owns it", "enter")

	assertDecision(t, only(t, root), map[string]any{
		"entry_id": "q-aaaaaaaaaaa2", "verdict": "skip", "reason": "Lee owns it", "at": "2026-10-20T09:30:00Z",
	})
	if view := m.View(); !strings.Contains(view, "changed to skip") {
		t.Errorf("the status does not say the decision changed:\n%s", view)
	}

	press(t, m, "a")
	assertDecision(t, only(t, root), map[string]any{"entry_id": "q-aaaaaaaaaaa2", "verdict": "approve", "at": "2026-10-20T09:30:00Z"})
}

func TestChangingOneDecisionKeepsTheOthers(t *testing.T) {
	root := fixtureState(t)
	press(t, start(t, root), "a", "j", "a", "k", "s", "enter")

	ds := decisions(t, root)
	if len(ds) != 2 {
		t.Fatalf("want 2 decisions, got %v", ds)
	}
	assertDecision(t, ds[0], map[string]any{"entry_id": "q-aaaaaaaaaaa1", "verdict": "approve", "at": "2026-10-20T09:30:00Z"})
	assertDecision(t, ds[1], map[string]any{"entry_id": "q-aaaaaaaaaaa2", "verdict": "skip", "at": "2026-10-20T09:30:00Z"})
}

func TestADecisionTheExecutorActedOnCannotBeChanged(t *testing.T) {
	root := fixtureState(t)
	m := press(t, start(t, root), "a")
	appendTo(t, filepath.Join(root, "results.jsonl"), `{"entry_id": "q-aaaaaaaaaaa2", "outcome": "success", "at": "2026-10-20T09:31:00Z"}`+"\n")
	view := press(t, m, "s", "enter").View()

	assertDecision(t, only(t, root), map[string]any{"entry_id": "q-aaaaaaaaaaa2", "verdict": "approve", "at": "2026-10-20T09:30:00Z"})
	if !strings.Contains(view, "the executor already acted on it") || !strings.Contains(view, "✓ done") {
		t.Errorf("the view does not say the executor acted:\n%s", view)
	}
}

func TestEditingAgainStartsFromMyEarlierEdit(t *testing.T) {
	root := fixtureState(t)
	editor(t, `sed 's/per request/per request on purpose/' "$1" > "$1.new" && mv "$1.new" "$1"`)
	press(t, start(t, root), "e", "enter", "e", "enter")

	assertDecision(t, only(t, root), map[string]any{
		"entry_id": "q-aaaaaaaaaaa2", "verdict": "approve",
		"draft": "It is per request on purpose on purpose so a stale entry cannot outlive a deploy.", "at": "2026-10-20T09:30:00Z",
	})
}

func TestViewingAnEntryShowsItsContextAndDraft(t *testing.T) {
	root := fixtureState(t)
	m := press(t, start(t, root), "j")
	if strings.Contains(m.View(), "exponential backoff") {
		t.Fatal("the list shows context before the entry is opened")
	}

	view := press(t, m, "enter").View()
	for _, want := range []string{
		"Dana asks for a review of 'Retry flaky uploads'",
		"https://github.com/acme/app/pull/12",
		"gh_review",
		"Looks good overall; one question on the backoff cap.",
		"dana",
		"Uploads now retry three times with exponential backoff, capped at 30s.",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("entry view lacks %q:\n%s", want, view)
		}
	}

	if strings.Contains(press(t, m, "esc").View(), "exponential backoff") {
		t.Error("esc does not go back to the list")
	}
}

func TestDecidingFromTheEntryView(t *testing.T) {
	root := fixtureState(t)
	press(t, start(t, root), "j", "enter", "a")

	assertDecision(t, only(t, root), map[string]any{"entry_id": "q-aaaaaaaaaaa1", "verdict": "approve", "at": "2026-10-20T09:30:00Z"})
}

// executor points PROC_EXECUTOR at a script standing in for claude-skills' executor and returns where it leaves a
// mark when it runs.
func executor(t *testing.T, script string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "executor")
	mark := filepath.Join(dir, "ran")
	body := "#!/bin/sh\n" + script + "\necho \"$XDG_STATE_HOME\" > " + mark + "\n"
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PROC_EXECUTOR", path)
	return mark
}

func waitFor(t *testing.T, path string) []byte {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
			return data
		}
	}
	t.Fatalf("%s never appeared", path)
	return nil
}

func TestQuittingStartsTheExecutorAndItsResultsShowNextTime(t *testing.T) {
	root := fixtureState(t)
	mark := executor(t, `sleep 0.2; echo '{"entry_id": "q-aaaaaaaaaaa2", "outcome": "success", "link": "https://github.com/acme/app/pull/9#discussion_r78", "at": "2026-10-20T09:31:00Z"}' >> "$XDG_STATE_HOME/process/results.jsonl"`)
	press(t, start(t, root), "a", "q")

	if got := strings.TrimSpace(string(waitFor(t, mark))); got != filepath.Dir(root) {
		t.Errorf("executor ran on XDG_STATE_HOME=%q, want proc's %q", got, filepath.Dir(root))
	}
	view := start(t, root).View()
	if !strings.Contains(view, "✓ done https://github.com/acme/app/pull/9#discussion_r78") {
		t.Errorf("the executor's result is not next to its entry:\n%s", view)
	}
}

func TestQuittingWithNothingDecidedLeavesTheExecutorAlone(t *testing.T) {
	root := fixtureState(t)
	mark := executor(t, "")
	press(t, start(t, root), "q")

	time.Sleep(300 * time.Millisecond)
	if _, err := os.Stat(mark); err == nil {
		t.Fatal("the executor ran with nothing to do")
	}
}

func TestAFailedActionShowsItsError(t *testing.T) {
	root := fixtureState(t)
	press(t, start(t, root), "j", "a")
	result := `{"entry_id": "q-aaaaaaaaaaa1", "outcome": "failure", "error": "review already submitted", "at": "2026-10-20T09:31:00Z"}` + "\n"
	if err := os.WriteFile(filepath.Join(root, "results.jsonl"), []byte(result), 0o644); err != nil {
		t.Fatal(err)
	}

	if view := start(t, root).View(); !strings.Contains(view, "✗ failed: review already submitted") {
		t.Errorf("the failure is not next to its entry:\n%s", view)
	}
}

func TestASkipAloneStillStartsTheExecutorToLearnFromItsReason(t *testing.T) {
	root := fixtureState(t)
	mark := executor(t, "")
	press(t, start(t, root), "s", "bot noise", "enter", "q")

	waitFor(t, mark)
}

func TestEntriesDecidedOverAWeekAgoLeaveTheList(t *testing.T) {
	root := fixtureState(t)
	old := `{"entry_id": "q-aaaaaaaaaaa2", "verdict": "approve", "at": "2026-10-12T09:00:00Z"}` + "\n"
	if err := os.WriteFile(filepath.Join(root, "decisions.jsonl"), []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	press(t, start(t, root), "a")

	ds := decisions(t, root)
	if len(ds) != 2 || ds[1]["entry_id"] != "q-aaaaaaaaaaa1" {
		t.Fatalf("want the first listed entry to be q-aaaaaaaaaaa1, decisions %v", ds)
	}
}

// testdata/urgency is what claude-skills' `process run --fixtures` wrote, so it checks the vendored schema too.
func TestFixtureQueuesFollowTheSharedSchema(t *testing.T) {
	for _, path := range []string{"testdata/state/process/queue.jsonl", "testdata/urgency/process/queue.jsonl"} {
		var entries []map[string]any
		if err := readJSONL(path, &entries); err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if err := validate("queue_entry", e); err != nil {
				t.Errorf("%s: %s: %v", path, e["id"], err)
			}
		}
	}
}

func TestAnExecutorThatIsNotInstalledIsReportedNotStarted(t *testing.T) {
	root := fixtureState(t)
	t.Setenv("PROC_EXECUTOR", "proc-test-no-such-executor --run")
	m := press(t, start(t, root), "a", "q").(*model)

	if m.executorErr == nil || m.executorLog != "" {
		t.Fatalf("want a start error and no log, got log %q, error %v", m.executorLog, m.executorErr)
	}
	only(t, root) // the decision is kept for the next quit
}

// requeue appends a new state of a fixture entry to the queue, as triage would, changed by change.
func requeue(t *testing.T, root, id string, change func(map[string]any)) {
	t.Helper()
	var entries []map[string]any
	if err := readJSONL(filepath.Join(root, "queue.jsonl"), &entries); err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e["id"] == id {
			change(e)
			line, _ := json.Marshal(e)
			appendTo(t, filepath.Join(root, "queue.jsonl"), string(line)+"\n")
			return
		}
	}
	t.Fatalf("no fixture entry %s", id)
}

func appendTo(t *testing.T, path, text string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(text); err != nil {
		t.Fatal(err)
	}
}

func noDecisions(t *testing.T, root string) {
	t.Helper()
	if ds := decisions(t, root); len(ds) != 0 {
		t.Fatalf("want no decision, got %v", ds)
	}
}

func TestClearingTheDraftRecordsNothing(t *testing.T) {
	root := fixtureState(t)
	editor(t, `: > "$1"`)
	press(t, start(t, root), "e", "enter")

	noDecisions(t, root)
}

func TestAnEntryTriageApprovedSilentlyCannotBeDecided(t *testing.T) {
	root := fixtureState(t)
	requeue(t, root, "q-aaaaaaaaaaa2", func(e map[string]any) { e["status"] = "approved" })
	editor(t, `echo changed > "$1"`)
	press(t, start(t, root), "s", "enter", "a", "p", "e", "enter")

	noDecisions(t, root)
}

func TestEscapingTheReasonPromptKeepsTheEditForTheNextOpen(t *testing.T) {
	root := fixtureState(t)
	once := filepath.Join(t.TempDir(), "edited")
	editor(t, `[ -e `+once+` ] || { sed 's/per request/per request on purpose/' "$1" > "$1.new" && mv "$1.new" "$1" && touch `+once+`; }`)
	press(t, start(t, root), "e", "esc", "e", "enter")

	assertDecision(t, only(t, root), map[string]any{
		"entry_id": "q-aaaaaaaaaaa2", "verdict": "approve",
		"draft": "It is per request on purpose so a stale entry cannot outlive a deploy.", "at": "2026-10-20T09:30:00Z",
	})
}

func TestAHalfWrittenLastLineIsLeftForItsWriterToFinish(t *testing.T) {
	root := fixtureState(t)
	appendTo(t, filepath.Join(root, "queue.jsonl"), `{"id": "q-cccccccccc`)
	appendTo(t, filepath.Join(root, "results.jsonl"), `{"entry_id": "q-aaaa`)
	press(t, start(t, root), "a")

	assertDecision(t, only(t, root), map[string]any{"entry_id": "q-aaaaaaaaaaa2", "verdict": "approve", "at": "2026-10-20T09:30:00Z"})
}

func TestTheListScrollsToKeepTheCursorOnScreen(t *testing.T) {
	root := fixtureState(t)
	m := deliver(start(t, root), tea.WindowSizeMsg{Width: 100, Height: 6})
	view := press(t, m, "j", "j").View()

	if !strings.Contains(view, "▌") || !strings.Contains(view, "ENG-7") {
		t.Errorf("the cursor's row is off screen:\n%s", view)
	}
	if n := strings.Count(view, "\n") + 1; n > 6 {
		t.Errorf("view is %d lines on a 6-line terminal:\n%s", n, view)
	}
}

func TestControlSequencesFromServicesNeverReachTheTerminal(t *testing.T) {
	colors(t)
	root := fixtureState(t)
	requeue(t, root, "q-aaaaaaaaaaa2", func(e map[string]any) {
		e["summary"] = "Lee asked\x1b]52;c;ZXZpbA==\x07 why"
		e["action"].(map[string]any)["draft"] = "Fine.\x1b[2J\x1b[Hreally"
	})
	m := start(t, root)

	for _, view := range []string{m.View(), press(t, m, "enter").View()} {
		for _, injected := range []string{"\x1b]52", "\x07", "\x1b[2J", "\x1b[H"} {
			if strings.Contains(view, injected) {
				t.Errorf("view carries %q from a service: %q", injected, view)
			}
		}
	}
}

func TestLongLinesWrapToTheTerminalWidth(t *testing.T) {
	colors(t)
	root := fixtureState(t)
	requeue(t, root, "q-aaaaaaaaaaa2", func(e map[string]any) {
		e["summary"] = strings.Repeat("Lee asked why the cache is per request ", 4)
		e["action"].(map[string]any)["draft"] = strings.Repeat("Because each request carries its own tenant. ", 6) + "END"
	})
	m := deliver(start(t, root), tea.WindowSizeMsg{Width: 40, Height: 100})

	for _, view := range []string{m.View(), press(t, m, "enter").View()} {
		for _, l := range strings.Split(view, "\n") {
			if w := ansi.StringWidth(l); w > 40 {
				t.Errorf("line is %d cells on a 40-cell terminal: %q", w, ansi.Strip(l))
			}
		}
	}
	if view := ansi.Strip(m.View()); !strings.Contains(view, "END") {
		t.Errorf("the end of the draft was cut instead of wrapped:\n%s", view)
	}
}

// colors renders the view in color, as on a terminal, for the rest of the test.
func colors(t *testing.T) {
	was := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI)
	t.Cleanup(func() { lipgloss.SetColorProfile(was) })
}

func TestADraftEndingInANewlineLeftAsIsIsUnchanged(t *testing.T) {
	root := fixtureState(t)
	requeue(t, root, "q-aaaaaaaaaaa2", func(e map[string]any) { e["action"].(map[string]any)["draft"] = "Sure.\n" })
	editor(t, `true`)
	press(t, start(t, root), "e", "enter")

	noDecisions(t, root)
}

func TestAPastedReasonStaysOneLine(t *testing.T) {
	root := fixtureState(t)
	m := press(t, start(t, root), "s")
	m = deliver(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("not mine\r\nLee owns it"), Paste: true})
	press(t, m, "enter")

	assertDecision(t, only(t, root), map[string]any{
		"entry_id": "q-aaaaaaaaaaa2", "verdict": "skip", "reason": "not mine  Lee owns it", "at": "2026-10-20T09:30:00Z",
	})
}

func TestCtrlCQuitsWithoutStartingTheExecutor(t *testing.T) {
	root := fixtureState(t)
	mark := executor(t, "")
	press(t, start(t, root), "a", "ctrl+c")

	time.Sleep(300 * time.Millisecond)
	if _, err := os.Stat(mark); err == nil {
		t.Fatal("ctrl+c started the executor")
	}
}

func TestDecidingDropsAnEditLeftAtTheReasonPrompt(t *testing.T) {
	root := fixtureState(t)
	editor(t, `echo changed > "$1"`)
	press(t, start(t, root), "e", "esc", "s", "enter")

	if left, _ := os.ReadDir(filepath.Join(root, "proc")); len(left) != 0 {
		t.Errorf("draft files left in the state directory: %v", left)
	}
}

func TestAWideTerminalShowsTheEntryUnderTheCursorBesideTheList(t *testing.T) {
	colors(t)
	root := fixtureState(t)
	m := deliver(start(t, root), tea.WindowSizeMsg{Width: 140, Height: 30})
	view := press(t, m, "j").View()

	for _, want := range []string{"ENG-7", "Looks good overall; one question on the backoff cap.", "Uploads now retry three times with exponential backoff"} {
		if !strings.Contains(view, want) {
			t.Errorf("wide view lacks %q:\n%s", want, ansi.Strip(view))
		}
	}
	lines := strings.Split(view, "\n")
	if len(lines) != 30 {
		t.Errorf("view is %d lines on a 30-line terminal", len(lines))
	}
	for _, l := range lines {
		if w := ansi.StringWidth(l); w > 140 {
			t.Errorf("line is %d cells on a 140-cell terminal: %q", w, ansi.Strip(l))
		}
	}
}
