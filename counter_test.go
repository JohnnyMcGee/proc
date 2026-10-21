package main

import "testing"

func TestCounterShowsPendingAndUrgentEntries(t *testing.T) {
	root := fixtureState(t)
	if got := counter(t, root); got != "proc 3 !1" {
		t.Fatalf("counter = %q, want %q", got, "proc 3 !1")
	}
}

func counter(t *testing.T, root string) string {
	t.Helper()
	line, err := countLine(root, now)
	if err != nil {
		t.Fatal(err)
	}
	return line
}

func TestADecidedEntryLeavesTheCounter(t *testing.T) {
	root := fixtureState(t)
	if err := appendDecision(root, Decision{EntryID: "q-aaaaaaaaaaa2", Verdict: "approve", At: stamp(now)}); err != nil {
		t.Fatal(err)
	}
	if got := counter(t, root); got != "proc 2" {
		t.Fatalf("counter = %q, want %q", got, "proc 2")
	}
}

func TestTheCounterIsEmptyWhenNothingWaits(t *testing.T) {
	if got := counter(t, t.TempDir()); got != "" {
		t.Fatalf("counter = %q, want it empty", got)
	}
}
