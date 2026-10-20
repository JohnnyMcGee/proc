package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
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
// bubbletea runtime would. Named keys are "enter", "esc" and "backspace"; anything else is typed as runes.
func press(t *testing.T, m tea.Model, keys ...string) tea.Model {
	t.Helper()
	for _, key := range keys {
		var msg tea.KeyMsg
		switch key {
		case "enter":
			msg = tea.KeyMsg{Type: tea.KeyEnter}
		case "esc":
			msg = tea.KeyMsg{Type: tea.KeyEsc}
		case "backspace":
			msg = tea.KeyMsg{Type: tea.KeyBackspace}
		default:
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}
		}
		m = deliver(m, msg)
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
