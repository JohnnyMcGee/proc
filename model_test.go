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
)

var now = time.Date(2026, 10, 20, 9, 30, 0, 0, time.UTC)

// fixtureState copies testdata/state into a scratch XDG_STATE_HOME and returns the process state directory.
func fixtureState(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	if err := os.CopyFS(home, os.DirFS("testdata/state")); err != nil {
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
	editor(t, `sed -i 's/per request/per request on purpose/' "$1"`)
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

func TestADecidedEntryCannotBeDecidedAgain(t *testing.T) {
	root := fixtureState(t)
	press(t, start(t, root), "a", "s", "enter", "p")
	press(t, start(t, root), "a")

	only(t, root)
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

func TestFixtureQueueFollowsTheSharedSchema(t *testing.T) {
	var entries []map[string]any
	if err := readJSONL("testdata/state/process/queue.jsonl", &entries); err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if err := validate("queue_entry", e); err != nil {
			t.Errorf("%s: %v", e["id"], err)
		}
	}
}
