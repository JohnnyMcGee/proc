package main

import (
	"errors"
	"maps"
	"os"
	"os/exec"
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
		"proc-brief.service": {`ExecStart="/opt/bin/proc" sweep` + "\n", `Environment="PATH=/opt/bin:/usr/bin"`},
		"proc-delta.service": {`ExecStart="/opt/bin/proc" sweep --delta` + "\n", `Environment="PATH=/opt/bin:/usr/bin"`},
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

func TestUninstallRemovesATmuxConfigInstallCreated(t *testing.T) {
	i, _ := setup(t)
	if err := i.install(); err != nil {
		t.Fatal(err)
	}
	if err := i.uninstall(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(i.home, ".tmux.conf")); !os.IsNotExist(err) {
		t.Fatalf("want no ~/.tmux.conf after uninstall, as before install; stat: %v", err)
	}
}

func TestInstallNamesTheProgramsTheTimedRunsWillNotFind(t *testing.T) {
	i, _ := setup(t)
	bin := t.TempDir()
	write(bin, "process", "#!/bin/sh\n")
	if err := os.Chmod(filepath.Join(bin, "process"), 0o755); err != nil {
		t.Fatal(err)
	}
	i.path = bin
	if got, want := i.missing(), []string{"claude", "notify-send"}; !slices.Equal(got, want) {
		t.Fatalf("missing = %q, want %q", got, want)
	}
}

func TestTheServicesUseTheStateAndConfigDirectoriesMyShellDoes(t *testing.T) {
	i, _ := setup(t)
	i.env = map[string]string{"XDG_STATE_HOME": "/data/state", "XDG_CONFIG_HOME": "/data/100%config"}
	if err := i.install(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"proc-brief.service", "proc-delta.service"} {
		unit := read(t, i.unitDir(), name)
		for _, line := range []string{`Environment="XDG_STATE_HOME=/data/state"`, `Environment="XDG_CONFIG_HOME=/data/100%%config"`} {
			if !strings.Contains(unit, line) {
				t.Errorf("%s lacks %q:\n%s", name, line, unit)
			}
		}
	}
}

func TestReSourcingTmuxConfigKeepsOneCounterAndItsWidth(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux is not installed")
	}
	i, _ := setup(t)
	// My config sets status-right but not its length, and I re-source it (bind r source-file ~/.tmux.conf).
	write(i.home, ".tmux.conf", "set -g status-right '%H:%M'\n")
	if err := i.install(); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(t.TempDir(), "tmux")
	tmux := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("tmux", append([]string{"-S", socket, "-f", filepath.Join(i.home, ".tmux.conf")}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("tmux %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	tmux("new-session", "-d")
	defer tmux("kill-server")
	for range 3 {
		tmux("source-file", filepath.Join(i.home, ".tmux.conf"))
	}
	if got := tmux("show", "-gv", "status-right"); got != "%H:%M #(/opt/bin/proc count)" {
		t.Errorf("status-right = %q, want the counter once", got)
	}
	if got := tmux("show", "-gv", "status-right-length"); got != "53" {
		t.Errorf("status-right-length = %s, want tmux's 40 widened once by 13", got)
	}
}

func TestUnitsKeepPathsWithSpacesQuotesAndBackslashesWhole(t *testing.T) {
	i, _ := setup(t)
	i.exe = `/opt/my bin/proc`
	i.path = `/opt/a"b:/opt/c\d`
	if err := i.install(); err != nil {
		t.Fatal(err)
	}
	unit := read(t, i.unitDir(), "proc-delta.service")
	for _, line := range []string{`ExecStart="/opt/my bin/proc" sweep --delta` + "\n", `Environment="PATH=/opt/a\"b:/opt/c\\d"`} {
		if !strings.Contains(unit, line) {
			t.Errorf("proc-delta.service lacks %q:\n%s", line, unit)
		}
	}
}

func TestAValueThatWouldBreakAUnitIsRefusedBeforeAnythingIsWritten(t *testing.T) {
	i, _ := setup(t)
	i.env = map[string]string{"XDG_STATE_HOME": "/data\nExecStartPre=/bin/false"}
	if err := i.install(); err == nil {
		t.Fatal("want the newline refused")
	}
	if _, err := os.Stat(i.unitDir()); !os.IsNotExist(err) {
		t.Fatalf("units were written: %v", err)
	}
}
