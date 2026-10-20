package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// Notified is one line of proc/notified.jsonl: an entry I have already been interrupted for.
type Notified struct {
	EntryID string `json:"entry_id"`
	At      string `json:"at"`
}

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
	for _, e := range entries {
		waiting := e.Status == "pending" || e.Status == "info"
		if e.Urgency != "urgent" || !waiting || done[e.ID] {
			continue
		}
		if err := send("proc: urgent · "+e.Project, e.Summary+"\n"+e.Link); err != nil {
			return err
		}
		if err := appendJSONL(path, Notified{EntryID: e.ID, At: stamp(now)}); err != nil {
			return err
		}
	}
	return nil
}

// notifySend shows a desktop notification that stays until I dismiss it.
func notifySend(title, body string) error {
	return exec.Command("notify-send", "--urgency=critical", "--app-name=proc", title, body).Run()
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
