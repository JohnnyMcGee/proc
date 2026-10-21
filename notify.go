package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// Notified is one line of proc/notified.jsonl: an entry I have already been interrupted for.
type Notified struct {
	EntryID string `json:"entry_id"`
	At      string `json:"at"`
}

// interruptFor is how long an urgent entry may still interrupt me. An older one is in the brief already; without this,
// the first run after install (or after notified.jsonl is lost) would raise one notification per urgent entry ever.
const interruptFor = 24 * time.Hour

// notifyUrgent interrupts me for urgent entries that still wait on me (to decide, or to read), once each. Triage
// decides what is urgent (the profile's urgency rules: my manager, incident channels, deploy-blocking reviews); proc
// only delivers it.
func notifyUrgent(root string, now time.Time, send func(title, body string) error) error {
	entries, err := loadQueue(root)
	if err != nil {
		return err
	}
	path := filepath.Join(root, "proc", "notified.jsonl")
	var notified []Notified
	if err := readJSONL(path, &notified); err != nil {
		return err
	}
	var decisions []Decision
	if err := readJSONL(filepath.Join(root, "decisions.jsonl"), &decisions); err != nil {
		return err
	}
	done := map[string]bool{}
	for _, n := range notified {
		done[n.EntryID] = true
	}
	for _, d := range decisions {
		done[d.EntryID] = true
	}
	var failed []error
	for _, e := range entries {
		waiting := e.Status == "pending" || e.Status == "info"
		created, err := time.Parse(time.RFC3339, e.Created)
		stale := err == nil && now.Sub(created) > interruptFor
		if e.Urgency != "urgent" || !waiting || stale || done[e.ID] {
			continue
		}
		if err := send("proc: urgent · "+e.Project, e.Summary+"\n"+e.Link); err != nil {
			failed = append(failed, err) // retried next time; the others still go now
			continue
		}
		if err := appendJSONL(path, Notified{EntryID: e.ID, At: stamp(now)}); err != nil {
			return err
		}
	}
	return errors.Join(failed...)
}

// notifySend shows a desktop notification that stays until I dismiss it.
func notifySend(title, body string) error {
	return runCommand("notify-send", "--urgency=critical", "--app-name=proc", "--", title, body) // a summary may start with -
}

func appendJSONL(path string, v any) error {
	line, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
