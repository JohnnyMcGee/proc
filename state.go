package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
	var entries []Entry
	if err := readJSONL(filepath.Join(root, "queue.jsonl"), &entries); err != nil {
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

	latest := map[string]int{}
	var order []string
	for i, e := range entries {
		if _, seen := latest[e.ID]; !seen {
			order = append(order, e.ID)
		}
		latest[e.ID] = i
	}
	var rows []Row
	for _, id := range order {
		e := entries[latest[id]]
		row := Row{Entry: e, Decision: decided[id], Result: resulted[id]}
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

func readJSONL[T any](path string, out *[]T) error {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
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
