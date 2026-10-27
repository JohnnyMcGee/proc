package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"time"
)

// The state directory is proc's only input and output: triage writes the queue, proc appends decisions, the executor
// appends results. Every file is append-only JSONL whose last line for an entry id is that entry's current state.

type Action struct {
	Type   string         `json:"type"`
	Params map[string]any `json:"params"`
	Draft  string         `json:"draft,omitempty"`
}

type Entry struct {
	ID      string   `json:"id"`
	Project string   `json:"project"`
	ItemIDs []string `json:"item_ids"`
	Kind    string   `json:"kind"`
	Summary string   `json:"summary"`
	Link    string   `json:"link"`
	Urgency string   `json:"urgency"`
	Action  Action   `json:"action"`
	Status  string   `json:"status"`
	Created string   `json:"created"`
}

type Decision struct {
	EntryID         string `json:"entry_id"`
	Verdict         string `json:"verdict"`
	Draft           string `json:"draft,omitempty"`
	Reason          string `json:"reason,omitempty"`
	PromoteToSilent bool   `json:"promote_to_silent,omitempty"`
	At              string `json:"at"`
}

type Result struct {
	EntryID string `json:"entry_id"`
	Outcome string `json:"outcome"`
	Link    string `json:"link,omitempty"`
	Error   string `json:"error,omitempty"`
	At      string `json:"at"`
}

type Item struct {
	ID      string `json:"id"`
	Summary string `json:"summary"`
	Context string `json:"context"`
	Actor   struct {
		Name string `json:"name"`
	} `json:"actor"`
}

// Row is one queue entry as proc shows it: the entry with its decision and result, if any.
type Row struct {
	Entry
	Decision *Decision
	Result   *Result
}

// stateRoot is $XDG_STATE_HOME/process, the directory claude-skills' process CLI writes.
func stateRoot() (string, error) {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "process"), nil
}

var urgencyRank = map[string]int{"urgent": 0, "normal": 1, "low": 2}

// shownFor is how long a decided entry stays on the list, so its result can be seen.
const shownFor = 7 * 24 * time.Hour

// loadRows returns what is waiting on me or on the executor, and what I decided in the last week, grouped by project
// and then by urgency.
func loadRows(root string, now time.Time) ([]Row, error) {
	entries, err := loadQueue(root)
	if err != nil {
		return nil, err
	}
	var decisions []Decision
	if err := readJSONL(filepath.Join(root, "decisions.jsonl"), &decisions); err != nil {
		return nil, err
	}
	var results []Result
	if err := readJSONL(filepath.Join(root, "results.jsonl"), &results); err != nil {
		return nil, err
	}
	decided := map[string]*Decision{}
	for i := range decisions {
		decided[decisions[i].EntryID] = &decisions[i]
	}
	resulted := map[string]*Result{}
	for i := range results {
		resulted[results[i].EntryID] = &results[i]
	}

	var rows []Row
	for _, e := range entries {
		row := Row{Entry: e, Decision: decided[e.ID], Result: resulted[e.ID]}
		if row.Decision != nil {
			at, err := time.Parse(time.RFC3339, row.Decision.At)
			if err == nil && now.Sub(at) < shownFor {
				rows = append(rows, row)
			}
		} else if e.Status == "pending" || e.Status == "approved" {
			rows = append(rows, row)
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Project != rows[j].Project {
			return rows[i].Project < rows[j].Project
		}
		return urgencyRank[rows[i].Urgency] < urgencyRank[rows[j].Urgency]
	})
	return rows, nil
}

// loadQueue returns each queue entry's current state, its last line, in the order the entries first appeared.
func loadQueue(root string) ([]Entry, error) {
	var lines []Entry
	if err := readJSONL(filepath.Join(root, "queue.jsonl"), &lines); err != nil {
		return nil, err
	}
	latest := map[string]int{}
	var entries []Entry
	for _, e := range lines {
		if i, seen := latest[e.ID]; seen {
			entries[i] = e
			continue
		}
		latest[e.ID] = len(entries)
		entries = append(entries, e)
	}
	return entries, nil
}

// loadItems reads the items a queue entry merged, for their context, from the profile's item store.
func loadItems(root, project string, ids []string) ([]Item, error) {
	var all []Item
	if err := readJSONL(filepath.Join(root, "profiles", project, "items.jsonl"), &all); err != nil {
		return nil, err
	}
	var items []Item
	for _, id := range ids {
		for _, item := range all {
			if item.ID == id {
				items = append(items, item)
				break
			}
		}
	}
	return items, nil
}

// appendDecision validates a decision against the shared schema before it reaches the decisions file.
func appendDecision(root string, d Decision) error {
	if err := validate("decision", d); err != nil {
		return err
	}
	line, err := json.Marshal(d)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(root, "decisions.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// replaceDecision puts a changed decision in place of the entry's earlier one, so the file keeps one decision per
// entry whichever line a reader takes. Only proc writes the file; the new one is renamed over it, so a reader sees
// the old file or the new one, never half of it.
func replaceDecision(root string, d Decision) error {
	if err := validate("decision", d); err != nil {
		return err
	}
	path := filepath.Join(root, "decisions.jsonl")
	var decisions []Decision
	if err := readJSONL(path, &decisions); err != nil {
		return err
	}
	var buf bytes.Buffer
	for _, kept := range append(slices.DeleteFunc(decisions, func(old Decision) bool { return old.EntryID == d.EntryID }), d) {
		line, err := json.Marshal(kept)
		if err != nil {
			return err
		}
		buf.Write(append(line, '\n'))
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// resultFor is the executor's result for an entry, read now: a sweep's executor may have acted since proc started.
func resultFor(root, id string) (*Result, error) {
	var results []Result
	if err := readJSONL(filepath.Join(root, "results.jsonl"), &results); err != nil {
		return nil, err
	}
	var found *Result
	for i := range results {
		if results[i].EntryID == id {
			found = &results[i]
		}
	}
	return found, nil
}

func readJSONL[T any](path string, out *[]T) error {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	// Writers append whole lines; anything after the last newline is a line still being written.
	data = data[:bytes.LastIndexByte(data, '\n')+1]
	for n, line := range splitLines(data) {
		var v T
		if err := json.Unmarshal(line, &v); err != nil {
			return fmt.Errorf("%s line %d: %w", path, n+1, err)
		}
		*out = append(*out, v)
	}
	return nil
}

func splitLines(data []byte) [][]byte {
	var lines [][]byte
	for _, line := range bytes.Split(data, []byte("\n")) {
		if len(bytes.TrimSpace(line)) > 0 {
			lines = append(lines, line)
		}
	}
	return lines
}

func stamp(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05Z")
}
