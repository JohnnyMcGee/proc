package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// fakeProcess stands in for `process run`: it prints, advances every watermark and rewrites the brief the way a run
// does, then fails with err (nil for a clean run).
type fakeProcess struct {
	root  string
	brief string
	err   error
	args  []string
}

func (p *fakeProcess) run(ctx context.Context, args []string, log io.Writer) error {
	p.args = args
	fmt.Fprintln(log, "acme: sweep-github")
	write(p.root, "profiles/acme/watermarks.json", `{"github": "2026-10-20T09:30:00Z", "linear": "2026-10-20T09:30:00Z"}`)
	write(p.root, "profiles/beta/watermarks.json", `{"linear": "2026-10-20T09:30:00Z"}`)
	brief := p.brief
	if brief == "" {
		brief = "# Brief — Tue 2026-10-20 09:30 · full\n\n## acme\n\nNothing new.\n"
	}
	write(p.root, "brief.md", brief)
	return p.err
}

func write(root, path, text string) {
	path = filepath.Join(root, path)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		panic(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		panic(err)
	}
}

func read(t *testing.T, root, path string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, path))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

const acmeMarks = `{"github": "2026-10-19T16:30:00Z"}`

func TestAFailedRunLeavesWatermarksUnchangedAndTheBriefLinksItsLog(t *testing.T) {
	root := fixtureState(t)
	write(root, "profiles/acme/watermarks.json", acmeMarks)
	p := &fakeProcess{root: root, err: errors.New("exit status 1")}
	if err := sweep(context.Background(), root, false, now, p.run, (&notifier{}).send); err == nil {
		t.Fatal("want the failed run reported")
	}
	if got := read(t, root, "profiles/acme/watermarks.json"); got != acmeMarks {
		t.Fatalf("acme watermarks = %s, want them as before the run: %s", got, acmeMarks)
	}
	if _, err := os.Stat(filepath.Join(root, "profiles/beta/watermarks.json")); !os.IsNotExist(err) {
		t.Fatalf("beta had no watermarks before the run; want none after, got %v", err)
	}
	log := regexp.MustCompile(`/\S+\.log`).FindString(read(t, root, "brief.md"))
	if log == "" {
		t.Fatalf("the brief links no log:\n%s", read(t, root, "brief.md"))
	}
	if got := read(t, "/", log); !strings.Contains(got, "acme: sweep-github") {
		t.Fatalf("the log %s does not hold the run's output:\n%s", log, got)
	}
}

func TestARunWithAFailedPhaseLeavesWatermarksUnchanged(t *testing.T) {
	root := fixtureState(t)
	write(root, "profiles/acme/watermarks.json", acmeMarks)
	p := &fakeProcess{root: root, brief: "# Brief — Tue 2026-10-20 09:30 · full\n\n## acme\n\n> ⚠ sweep-linear failed: MCP server not connected\nNothing new.\n"}
	if err := sweep(context.Background(), root, false, now, p.run, (&notifier{}).send); err == nil {
		t.Fatal("want the failed phase reported")
	}
	if got := read(t, root, "profiles/acme/watermarks.json"); got != acmeMarks {
		t.Fatalf("acme watermarks = %s, want them as before the run: %s", got, acmeMarks)
	}
	if brief := read(t, root, "brief.md"); !regexp.MustCompile(`/\S+\.log`).MatchString(brief) {
		t.Fatalf("the brief links no log:\n%s", brief)
	}
}

func TestACleanRunKeepsItsWatermarksAndNotifiesWhatItQueued(t *testing.T) {
	root := fixtureState(t)
	write(root, "profiles/acme/watermarks.json", acmeMarks)
	p := &fakeProcess{root: root}
	n := &notifier{}
	if err := sweep(context.Background(), root, true, now, p.run, n.send); err != nil {
		t.Fatal(err)
	}
	if strings.Join(p.args, " ") != "run --delta" {
		t.Fatalf("ran `process %s`, want `process run --delta`", strings.Join(p.args, " "))
	}
	if got := read(t, root, "profiles/acme/watermarks.json"); got == acmeMarks {
		t.Fatal("a clean run's watermarks were put back")
	}
	if brief := read(t, root, "brief.md"); strings.Contains(brief, "⚠") {
		t.Fatalf("a clean run's brief carries a warning:\n%s", brief)
	}
	if len(n.sent) != 1 {
		t.Fatalf("sent %q, want the urgent entry", n.sent)
	}
}

func TestARunStartedDuringAnotherWaitsForItToFinish(t *testing.T) {
	root := fixtureState(t)
	started, release := make(chan struct{}), make(chan struct{})
	first := func(ctx context.Context, args []string, log io.Writer) error {
		close(started)
		<-release
		return (&fakeProcess{root: root}).run(ctx, args, log)
	}
	done := make(chan error)
	go func() { done <- sweep(context.Background(), root, false, now, first, (&notifier{}).send) }()
	<-started
	overlapped := make(chan bool, 1)
	second := func(ctx context.Context, args []string, log io.Writer) error {
		select {
		case <-release:
			overlapped <- false
		default:
			overlapped <- true
		}
		return (&fakeProcess{root: root}).run(ctx, args, log)
	}
	go func() { done <- sweep(context.Background(), root, true, now, second, (&notifier{}).send) }()
	time.Sleep(50 * time.Millisecond)
	close(release)
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if <-overlapped {
		t.Fatal("the second run started while the first was still running")
	}
}

// hung stands in for a run that never finishes on its own: it advances the watermarks, then waits to be stopped.
func hung(root string) func(ctx context.Context, args []string, log io.Writer) error {
	return func(ctx context.Context, args []string, log io.Writer) error {
		write(root, "profiles/acme/watermarks.json", `{"github": "2026-10-20T09:30:00Z"}`)
		<-ctx.Done()
		return ctx.Err()
	}
}

func TestARunThatHangsIsStoppedAndRetriedNextTime(t *testing.T) {
	root := fixtureState(t)
	write(root, "profiles/acme/watermarks.json", acmeMarks)
	defer func(was time.Duration) { runTimeout = was }(runTimeout)
	runTimeout = 50 * time.Millisecond
	if err := sweep(context.Background(), root, false, now, hung(root), (&notifier{}).send); err == nil {
		t.Fatal("want the hung run reported")
	}
	if got := read(t, root, "profiles/acme/watermarks.json"); got != acmeMarks {
		t.Fatalf("acme watermarks = %s, want them as before the run: %s", got, acmeMarks)
	}
	log := regexp.MustCompile(`/\S+\.log`).FindString(read(t, root, "brief.md"))
	if got := read(t, "/", log); !strings.Contains(got, "timed out") {
		t.Fatalf("the log does not say the run timed out:\n%s", got)
	}
}

func TestARunStoppedFromOutsideLeavesWatermarksUnchanged(t *testing.T) {
	root := fixtureState(t)
	write(root, "profiles/acme/watermarks.json", acmeMarks)
	ctx, stop := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, stop) // systemd stopping the unit, or the machine shutting down
	if err := sweep(ctx, root, false, now, hung(root), (&notifier{}).send); err == nil {
		t.Fatal("want the stopped run reported")
	}
	if got := read(t, root, "profiles/acme/watermarks.json"); got != acmeMarks {
		t.Fatalf("acme watermarks = %s, want them as before the run: %s", got, acmeMarks)
	}
}

func TestTheLogSaysWhyARunFailed(t *testing.T) {
	root := fixtureState(t)
	missing := func(ctx context.Context, args []string, log io.Writer) error {
		return errors.New(`exec: "process": executable file not found in $PATH`)
	}
	if err := sweep(context.Background(), root, false, now, missing, (&notifier{}).send); err == nil {
		t.Fatal("want the failed run reported")
	}
	log := regexp.MustCompile(`/\S+\.log`).FindString(read(t, root, "brief.md"))
	if got := read(t, "/", log); !strings.Contains(got, "executable file not found") {
		t.Fatalf("the log does not say why the run failed:\n%s", got)
	}
}
