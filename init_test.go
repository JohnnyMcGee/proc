package main

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/BurntSushi/toml"
)

// fakeCheckout is a claude-skills checkout holding every process skill and a process CLI that records each call in
// log. Its `profiles` refuses a profile with a channel that lacks its #, as the real one does; its `run` writes a
// brief to the state directory it was given.
func fakeCheckout(t *testing.T) (dir, log string) {
	t.Helper()
	dir, log = t.TempDir(), filepath.Join(t.TempDir(), "process.log")
	for _, skill := range []string{"brief", "execute", "next-work", "triage", "sweep-github", "sweep-slack", "afk"} {
		write(dir, skill+"/SKILL.md", "# "+skill+"\n")
	}
	write(dir, "process/process.py", `#!/bin/sh
echo "$*" >> `+log+`
case "$1" in
profiles)
	for f in "$XDG_CONFIG_HOME"/process/profiles/*.md; do
		if grep -Eq '^(channels|watchlist) = .*[[,] *"[^#]' "$f"; then
			printf 'process: profile %s:\n  $.urgency.channels[0]: does not match ^#\n' "$f"
			exit 1
		fi
	done
	echo "profiles ok";;
run)
	echo "state $XDG_STATE_HOME" >> `+log+`
	mkdir -p "$XDG_STATE_HOME/process"
	echo "# The dry-run brief" > "$XDG_STATE_HOME/process/brief.md";;
esac
`)
	if err := os.Chmod(filepath.Join(dir, "process/process.py"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir, log
}

// setupInit is `proc init` over a scratch home, answering from the lines given to answer and opening no editor.
func setupInit(t *testing.T) (*initializer, *bytes.Buffer) {
	t.Helper()
	home := t.TempDir()
	out := &bytes.Buffer{}
	return &initializer{
		home:       home,
		configHome: filepath.Join(home, ".config"),
		path:       filepath.Join(home, ".local/bin") + ":/usr/bin:/bin",
		out:        out,
		accessible: true,
		editor:     func(string) error { return nil },
		now:        func() time.Time { return now },
		zone:       "America/Chicago",
	}, out
}

func answer(i *initializer, lines ...string) {
	i.in = strings.NewReader(strings.Join(lines, "\n") + "\n")
}

// widgets answers every question for a profile named widgets on all six sources, and declines the dry run.
func widgets(checkout string) []string {
	return []string{
		checkout,
		"widgets", "", "0", // name, workdir (~/code/widgets), sources (all six; 0 confirms)
		"acme/widgets", "WID", "acme", "#incidents, #deploys", "me@acme.example", "mo@acme.example", "",
		"widgets", "#widgets", "@acme.example", // match
		"Mo, mo-acme", "#incidents, #incident-*", "deploy-blocking", // urgency
		"n", // no dry run
	}
}

func profile(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	parts := strings.SplitN(text, "+++\n", 3)
	if len(parts) != 3 {
		t.Fatalf("%s has no +++ front matter:\n%s", path, text)
	}
	var p map[string]any
	if _, err := toml.Decode(parts[1], &p); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return p
}

func TestInitOnAFreshHomeLinksTheSkillsInstallsProcessAndWritesTheProfile(t *testing.T) {
	i, out := setupInit(t)
	checkout, _ := fakeCheckout(t)
	answer(i, widgets(checkout)...)
	if err := i.init(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, skill := range []string{"brief", "execute", "next-work", "process", "triage", "sweep-github", "sweep-slack"} {
		link := filepath.Join(i.home, ".claude/skills", skill)
		if target, err := os.Readlink(link); err != nil || target != filepath.Join(checkout, skill) {
			t.Errorf("%s -> %q (%v), want a link to the checkout's %s", link, target, err, skill)
		}
	}
	if _, err := os.Lstat(filepath.Join(i.home, ".claude/skills/afk")); !os.IsNotExist(err) {
		t.Errorf("linked afk, which is not a process skill: %v", err)
	}
	process := filepath.Join(i.home, ".local/bin/process")
	if resolved, err := filepath.EvalSymlinks(process); err != nil || resolved != filepath.Join(checkout, "process/process.py") {
		t.Errorf("%s resolves to %q (%v), want the checkout's process.py", process, resolved, err)
	}

	got := profile(t, filepath.Join(i.home, ".config/process/profiles/widgets.md"))
	want := map[string]any{
		"name": "widgets", "active": true, "workdir": "~/code/widgets", "timezone": "America/Chicago",
		"sources": []any{"github", "linear", "slack", "gmail", "calendar", "meetings"},
		"github":  map[string]any{"repo": "acme/widgets", "review_skill": "/review"},
		"linear":  map[string]any{"team": "WID"},
		"slack":   map[string]any{"workspace": "acme", "watchlist": []any{"#incidents", "#deploys"}},
		"google":  map[string]any{"account": "me@acme.example", "manager": "mo@acme.example", "noise_label": "Noise"},
		"match":   map[string]any{"keywords": []any{"widgets"}, "channels": []any{"#widgets"}, "senders": []any{"@acme.example"}},
		"urgency": map[string]any{"people": []any{"Mo", "mo-acme"}, "channels": []any{"#incidents", "#incident-*"}, "labels": []any{"deploy-blocking"}},
	}
	for key, value := range want {
		if !reflect.DeepEqual(got[key], value) {
			t.Errorf("%s = %#v, want %#v", key, got[key], value)
		}
	}
	if text := read(t, i.home, ".config/process/profiles/widgets.md"); strings.Contains(text, "An example profile") {
		t.Errorf("the profile still calls itself the example:\n%s", text)
	}
	example := profile(t, "profiles/acme.md")
	for _, key := range []string{"phases", "autonomy", "afk"} {
		if !reflect.DeepEqual(got[key], example[key]) {
			t.Errorf("%s differs from the example profile's:\n got %#v\nwant %#v", key, got[key], example[key])
		}
	}
}

// tree is every file under dir with its content, and every link with its target.
func tree(t *testing.T, dir string) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		switch {
		case err != nil || d.IsDir():
		case d.Type()&os.ModeSymlink != 0:
			files[path], err = os.Readlink(path)
			files[path] = "-> " + files[path]
		default:
			files[path] = read(t, "/", path)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func TestASecondInitRemembersTheCheckoutSkipsTheInstallAndAddsAProfile(t *testing.T) {
	i, out := setupInit(t)
	checkout, _ := fakeCheckout(t)
	answer(i, widgets(checkout)...)
	if err := i.init(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	before := tree(t, i.home)
	out.Reset()
	answer(i, // no checkout: it is remembered
		"gadgets", "", "3", "4", "5", "6", "0", // github and linear only
		"acme/gadgets", "GAD", "", "", "", "", "", "",
		"n",
	)
	if err := i.init(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if strings.Contains(out.String(), "proc: linked") || !strings.Contains(out.String(), "already linked") {
		t.Errorf("want every install step reported as already done:\n%s", out)
	}
	after := tree(t, i.home)
	for path, text := range before {
		if after[path] != text {
			t.Errorf("the second init changed %s", path)
		}
	}
	got := profile(t, filepath.Join(i.home, ".config/process/profiles/gadgets.md"))
	if got["name"] != "gadgets" || !reflect.DeepEqual(got["sources"], []any{"github", "linear"}) || got["slack"] != nil || got["google"] != nil {
		t.Errorf("gadgets profile = %#v", got)
	}
}

func TestAnAnswerProcessRefusesShowsItsErrorAndAsksAgain(t *testing.T) {
	i, out := setupInit(t)
	t.Setenv("TMPDIR", t.TempDir())
	checkout, _ := fakeCheckout(t)
	lines := widgets(checkout)
	lines[15] = "incidents" // urgency channels, without the #
	lines = append(lines[:len(lines)-1],
		"", "0", "", "", "", "", "", "", "", "", "", "", "", // the same answers again, but for
		"#incidents", // the urgency channels
		"",
		"n",
	)
	answer(i, lines...)
	if err := i.init(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out.String(), "$.urgency.channels[0]: does not match ^#") {
		t.Errorf("did not show process profiles' error:\n%s", out)
	}
	got := profile(t, filepath.Join(i.home, ".config/process/profiles/widgets.md"))
	want := map[string]any{"people": []any{"Mo", "mo-acme"}, "channels": []any{"#incidents"}, "labels": []any{"deploy-blocking"}}
	if !reflect.DeepEqual(got["urgency"], want) {
		t.Errorf("urgency = %#v, want %#v", got["urgency"], want)
	}
	if scratch, _ := os.ReadDir(os.Getenv("TMPDIR")); len(scratch) > 0 {
		t.Errorf("left %v behind in TMPDIR", scratch)
	}
}

func TestStoppingAfterARefusedAnswerLeavesNoProfile(t *testing.T) {
	i, out := setupInit(t)
	t.Setenv("TMPDIR", t.TempDir())
	checkout, _ := fakeCheckout(t)
	lines := widgets(checkout)
	lines[15] = "incidents"
	answer(i, lines[:len(lines)-1]...)
	if err := i.init(); err == nil {
		t.Fatalf("want init to stop when the answers run out\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(i.home, ".config/process/profiles/widgets.md")); !os.IsNotExist(err) {
		t.Errorf("an invalid profile is on disk: %v", err)
	}
	if scratch, _ := os.ReadDir(os.Getenv("TMPDIR")); len(scratch) > 0 {
		t.Errorf("left %v behind in TMPDIR", scratch)
	}
}

func TestInitAsksBeforeReplacingAProfileAndKeepsItWhenToldTo(t *testing.T) {
	i, out := setupInit(t)
	checkout, _ := fakeCheckout(t)
	write(i.home, ".config/process/profiles/widgets.md", "my own profile\n")
	lines := widgets(checkout)
	answer(i, append([]string{checkout, "widgets", "n", "gizmos"}, lines[2:]...)...)
	if err := i.init(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if got := read(t, i.home, ".config/process/profiles/widgets.md"); got != "my own profile\n" {
		t.Errorf("widgets.md was replaced:\n%s", got)
	}
	if got := profile(t, filepath.Join(i.home, ".config/process/profiles/gizmos.md")); got["name"] != "gizmos" {
		t.Errorf("gizmos profile = %#v", got)
	}
}

func TestTheProfileOpensInTheEditorAndAnEditProcessRefusesOpensItAgain(t *testing.T) {
	i, out := setupInit(t)
	checkout, _ := fakeCheckout(t)
	answer(i, widgets(checkout)...)
	edits := []func(string) string{
		func(text string) string {
			return strings.Replace(text, `labels = ["deploy-blocking"]`, `channels = ["incidents"]`, 1)
		},
		func(text string) string {
			return strings.Replace(text, `channels = ["incidents"]`, `labels = ["deploy-blocking", "hotfix"]`, 1)
		},
	}
	var opened []string
	i.editor = func(path string) error {
		opened = append(opened, path)
		if len(opened) > len(edits) {
			t.Fatalf("opened the editor a %d time", len(opened))
		}
		write("/", path, edits[len(opened)-1](read(t, "/", path)))
		return nil
	}
	if err := i.init(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if len(opened) != 2 {
		t.Fatalf("opened the editor %d times, want twice: once, then again after the edit process refused", len(opened))
	}
	if !strings.Contains(out.String(), "does not match ^#") {
		t.Errorf("did not show process profiles' error:\n%s", out)
	}
	got := profile(t, filepath.Join(i.home, ".config/process/profiles/widgets.md"))
	if want := []any{"deploy-blocking", "hotfix"}; !reflect.DeepEqual(got["urgency"].(map[string]any)["labels"], want) {
		t.Errorf("urgency = %#v, want the edited labels %q", got["urgency"], want)
	}
}
