package main

import (
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// setup is an installer over a scratch home, recording the systemctl and tmux commands it would run.
func setup(t *testing.T) (*installer, *[]string) {
	t.Helper()
	home := t.TempDir()
	var ran []string
	return &installer{
		home:       home,
		configHome: filepath.Join(home, ".config"),
		exe:        "/opt/bin/proc",
		path:       "/opt/bin:/usr/bin",
		run: func(name string, args ...string) error {
			ran = append(ran, name+" "+strings.Join(args, " "))
			return nil
		},
	}, &ran
}

func TestInstallAddsTimersForEachSweepAndTheCounterToTmux(t *testing.T) {
	i, ran := setup(t)
	write(i.home, ".tmux.conf", "set -g mouse on\n")
	if err := i.install(); err != nil {
		t.Fatal(err)
	}
	units := filepath.Join(i.home, ".config/systemd/user")
	for file, want := range map[string][]string{
		"proc-brief.timer":   {"OnCalendar=*-*-* 08:00:00 America/Chicago", "Persistent=true", "WantedBy=timers.target"},
		"proc-delta.timer":   {"OnCalendar=*-*-* 12:30:00 America/Chicago", "OnCalendar=*-*-* 16:30:00 America/Chicago", "Persistent=true"},
		"proc-brief.service": {"ExecStart=/opt/bin/proc sweep\n", `Environment="PATH=/opt/bin:/usr/bin"`},
		"proc-delta.service": {"ExecStart=/opt/bin/proc sweep --delta\n", `Environment="PATH=/opt/bin:/usr/bin"`},
	} {
		unit := read(t, units, file)
		for _, line := range want {
			if !strings.Contains(unit, line) {
				t.Errorf("%s lacks %q:\n%s", file, line, unit)
			}
		}
	}
	snippet := filepath.Join(i.home, ".config/proc/proc.tmux.conf")
	if !strings.Contains(read(t, "/", snippet), "#(/opt/bin/proc count)") {
		t.Errorf("the tmux snippet does not show the counter:\n%s", read(t, "/", snippet))
	}
	if got, want := read(t, i.home, ".tmux.conf"), "set -g mouse on\nsource-file "+snippet+"\n"; got != want {
		t.Errorf(".tmux.conf = %q, want %q", got, want)
	}
	for _, cmd := range []string{"systemctl --user daemon-reload", "systemctl --user enable --now proc-brief.timer proc-delta.timer"} {
		if !slices.Contains(*ran, cmd) {
			t.Errorf("did not run %q; ran %q", cmd, *ran)
		}
	}
}

func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			files[path] = read(t, "/", path)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func TestInstallingTwiceChangesNothing(t *testing.T) {
	i, _ := setup(t)
	write(i.home, ".tmux.conf", "set -g mouse on")
	if err := i.install(); err != nil {
		t.Fatal(err)
	}
	once := snapshot(t, i.home)
	if err := i.install(); err != nil {
		t.Fatal(err)
	}
	if twice := snapshot(t, i.home); !maps.Equal(once, twice) {
		t.Fatalf("a second install changed files:\nonce:  %q\ntwice: %q", once, twice)
	}
}

func TestUninstallLeavesHomeAsItWasAndCanRunAgain(t *testing.T) {
	i, ran := setup(t)
	write(i.home, ".tmux.conf", "set -g mouse on\n")
	before := snapshot(t, i.home)
	if err := i.install(); err != nil {
		t.Fatal(err)
	}
	if err := i.uninstall(); err != nil {
		t.Fatal(err)
	}
	if after := snapshot(t, i.home); !maps.Equal(before, after) {
		t.Fatalf("uninstall left files changed:\nbefore: %q\nafter:  %q", before, after)
	}
	if !slices.Contains(*ran, "systemctl --user disable --now proc-brief.timer proc-delta.timer") {
		t.Fatalf("did not stop the timers; ran %q", *ran)
	}
	*ran = nil
	if err := i.uninstall(); err != nil {
		t.Fatalf("a second uninstall: %v", err)
	}
	if slices.ContainsFunc(*ran, func(cmd string) bool { return strings.Contains(cmd, "disable") }) {
		t.Fatalf("a second uninstall disabled timers that are gone; ran %q", *ran)
	}
}

func TestInstallShowsTheCounterInARunningTmuxAndCarriesOnWithoutOne(t *testing.T) {
	i, ran := setup(t)
	record := i.run
	i.run = func(name string, args ...string) error {
		record(name, args...)
		if name == "tmux" {
			return errors.New("no server running")
		}
		return nil
	}
	if err := i.install(); err != nil {
		t.Fatalf("install failed without a tmux server: %v", err)
	}
	if want := "tmux source-file " + i.snippetPath(); !slices.Contains(*ran, want) {
		t.Fatalf("did not run %q; ran %q", want, *ran)
	}
}
