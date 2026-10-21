package main

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// notifier records each notification instead of sending it.
type notifier struct{ sent []string }

func (n *notifier) send(title, body string) error {
	n.sent = append(n.sent, title+" | "+body)
	return nil
}

func notifyAll(t *testing.T, root string, n *notifier) {
	t.Helper()
	if err := notifyUrgent(root, now, n.send); err != nil {
		t.Fatal(err)
	}
}

func TestOnlyUrgentEntriesNotify(t *testing.T) {
	root := fixtureState(t)
	n := &notifier{}
	notifyAll(t, root, n)
	want := []string{"proc: urgent · acme | Lee asked why the cache is per request on your PR #9\nhttps://github.com/acme/app/pull/9#discussion_r77"}
	if !reflect.DeepEqual(n.sent, want) {
		t.Fatalf("sent %q, want %q", n.sent, want)
	}
}

func TestAnUrgentEntryNotifiesOnlyOnce(t *testing.T) {
	root := fixtureState(t)
	notifyAll(t, root, &notifier{})
	again := &notifier{}
	notifyAll(t, root, again)
	if len(again.sent) != 0 {
		t.Fatalf("notified again: %q", again.sent)
	}
}

// enqueue appends a queue entry the way triage would, with the fields notify and the counter read.
func enqueue(t *testing.T, root, id, summary, urgency, status string) {
	t.Helper()
	line, err := json.Marshal(map[string]any{
		"id": id, "project": "acme", "item_ids": []string{}, "sources": []string{"slack"}, "kind": "mention",
		"subject": "https://acme.slack.com/archives/C1", "summary": summary, "link": "https://acme.slack.com/archives/C1",
		"urgency": urgency, "autonomy": "draft", "action": map[string]any{"type": "none", "params": map[string]any{}},
		"status": status, "run": "2026-10-20T08:00:00Z", "created": "2026-10-20T08:05:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	appendTo(t, filepath.Join(root, "queue.jsonl"), string(line)+"\n")
}

// Triage decides what is urgent from the profile's rules (claude-skills#46 covers the rules themselves); these entries
// are what it queues for each rule and for asks that miss them.
func TestEveryUrgentEntryNotifiesAndNothingElseDoes(t *testing.T) {
	root := fixtureState(t)
	notifyAll(t, root, &notifier{}) // the fixture's own urgent entry
	enqueue(t, root, "q-0000000000a1", "My manager DMed me", "urgent", "pending")
	enqueue(t, root, "q-0000000000a2", "#incident-42 mentions me", "urgent", "info")
	enqueue(t, root, "q-0000000000a3", "Review blocks Friday's deploy", "urgent", "pending")
	enqueue(t, root, "q-0000000000b1", "A peer DMed me", "normal", "pending")
	enqueue(t, root, "q-0000000000b2", "#random chatter", "low", "info")
	n := &notifier{}
	notifyAll(t, root, n)
	want := []string{
		"proc: urgent · acme | My manager DMed me\nhttps://acme.slack.com/archives/C1",
		"proc: urgent · acme | #incident-42 mentions me\nhttps://acme.slack.com/archives/C1",
		"proc: urgent · acme | Review blocks Friday's deploy\nhttps://acme.slack.com/archives/C1",
	}
	if !reflect.DeepEqual(n.sent, want) {
		t.Fatalf("sent %q, want %q", n.sent, want)
	}
}

func TestAnUrgentEntryThatNoLongerWaitsOnMeDoesNotNotify(t *testing.T) {
	root := fixtureState(t)
	if err := appendDecision(root, Decision{EntryID: "q-aaaaaaaaaaa2", Verdict: "skip", At: stamp(now)}); err != nil {
		t.Fatal(err)
	}
	enqueue(t, root, "q-0000000000c1", "An older version of an urgent ask", "urgent", "superseded")
	enqueue(t, root, "q-0000000000c2", "Triage handled it silently", "urgent", "done")
	n := &notifier{}
	notifyAll(t, root, n)
	if len(n.sent) != 0 {
		t.Fatalf("sent %q, want nothing", n.sent)
	}
}

func TestANotificationThatFailedToSendIsRetried(t *testing.T) {
	root := fixtureState(t)
	broken := func(title, body string) error { return errors.New("no notification daemon") }
	if err := notifyUrgent(root, now, broken); err == nil {
		t.Fatal("want the failed send reported")
	}
	n := &notifier{}
	notifyAll(t, root, n)
	if len(n.sent) != 1 {
		t.Fatalf("sent %q, want the urgent entry retried once", n.sent)
	}
}

func TestAnUrgentEntryFromBeforeYesterdayDoesNotInterrupt(t *testing.T) {
	root := fixtureState(t)
	requeue(t, root, "q-aaaaaaaaaaa2", func(e map[string]any) { e["created"] = "2026-10-18T08:02:00Z" })
	n := &notifier{}
	notifyAll(t, root, n)
	if len(n.sent) != 0 {
		t.Fatalf("sent %q for an entry two days old, want nothing", n.sent)
	}
}

func TestOneNotificationThatFailsDoesNotHoldBackTheOthers(t *testing.T) {
	root := fixtureState(t)
	enqueue(t, root, "q-0000000000d1", "My manager DMed me", "urgent", "pending")
	var sent []string
	failFirst := func(title, body string) error {
		if strings.HasPrefix(body, "Lee") {
			return errors.New("notify-send: bad argument")
		}
		sent = append(sent, body)
		return nil
	}
	if err := notifyUrgent(root, now, failFirst); err == nil {
		t.Fatal("want the failed send reported")
	}
	if len(sent) != 1 || !strings.HasPrefix(sent[0], "My manager DMed me") {
		t.Fatalf("sent %q, want the DM despite the failure before it", sent)
	}
}
