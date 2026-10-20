package main

import (
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// installer puts proc's unattended side in place: systemd user timers that run `proc sweep`, and the tmux counter.
type installer struct {
	home       string
	configHome string
	exe        string            // proc's absolute path, which the units and the tmux snippet run
	path       string            // PATH for the services, so they find process, claude and notify-send as my shell does
	env        map[string]string // more of my shell's environment the services need: where the state and profiles are
	run        func(name string, args ...string) error
}

// newInstaller installs for me, running this binary, with the PATH of the shell that ran `proc install`.
func newInstaller() (*installer, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	configHome := os.Getenv("XDG_CONFIG_HOME")
	if configHome == "" {
		configHome = filepath.Join(home, ".config")
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return nil, err
	}
	env := map[string]string{}
	for _, name := range []string{"XDG_STATE_HOME", "XDG_CONFIG_HOME"} {
		if value := os.Getenv(name); value != "" {
			env[name] = value
		}
	}
	return &installer{home: home, configHome: configHome, exe: exe, path: os.Getenv("PATH"), env: env, run: runCommand}, nil
}

func runCommand(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Every sweep is America/Chicago time: the machine and my working day are there.
const sweepZone = "America/Chicago"

var timers = []string{"proc-brief.timer", "proc-delta.timer"}

func (i *installer) units() map[string]string {
	// systemd reads % as a specifier; %% is a literal %.
	escape := strings.NewReplacer("%", "%%").Replace
	env := fmt.Sprintf("Environment=\"PATH=%s\"\n", escape(i.path))
	for _, name := range slices.Sorted(maps.Keys(i.env)) {
		env += fmt.Sprintf("Environment=\"%s=%s\"\n", name, escape(i.env[name]))
	}
	service := func(what, args string) string {
		return fmt.Sprintf("[Unit]\nDescription=proc: %s\n\n[Service]\nType=oneshot\n%sExecStart=%s sweep%s\n",
			what, env, escape(i.exe), args)
	}
	timer := func(what string, times ...string) string {
		unit := fmt.Sprintf("[Unit]\nDescription=proc: %s\n\n[Timer]\n", what)
		for _, t := range times {
			unit += fmt.Sprintf("OnCalendar=*-*-* %s:00 %s\n", t, sweepZone)
		}
		// Persistent: a run missed while the machine was off fires as soon as it is back.
		return unit + "Persistent=true\n\n[Install]\nWantedBy=timers.target\n"
	}
	return map[string]string{
		"proc-brief.service": service("full sweep and brief", ""),
		"proc-brief.timer":   timer("full sweep and brief every morning", "08:00"),
		"proc-delta.service": service("delta sweep", " --delta"),
		"proc-delta.timer":   timer("delta sweeps through the day", "12:30", "16:30"),
	}
}

func (i *installer) unitDir() string { return filepath.Join(i.configHome, "systemd", "user") }

func (i *installer) snippetPath() string {
	return filepath.Join(i.configHome, "proc", "proc.tmux.conf")
}

// snippet appends the counter to tmux's status line once, however often the config is sourced, and widens the
// status line by as much so tmux does not cut the counter off.
func (i *installer) snippet() string {
	return "# proc's counter: entries waiting on me, then `!` and how many are urgent. `proc uninstall` removes it.\n" +
		fmt.Sprintf("if -F '#{m:*proc count*,#{status-right}}' '' 'set -ag status-right \" #(%s count)\" ; set -gF status-right-length \"#{e|+:#{status-right-length},%d}\"'\n", i.exe, len(" proc 999 !99"))
}

func (i *installer) sourceLine() string { return "source-file " + i.snippetPath() }

// tmuxConf is the config tmux reads: ~/.tmux.conf, else the XDG one, else a new ~/.tmux.conf.
func (i *installer) tmuxConf() string {
	for _, path := range []string{filepath.Join(i.home, ".tmux.conf"), filepath.Join(i.configHome, "tmux", "tmux.conf")} {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	return filepath.Join(i.home, ".tmux.conf")
}

func (i *installer) install() error {
	if err := os.MkdirAll(i.unitDir(), 0o755); err != nil {
		return err
	}
	for name, unit := range i.units() {
		if err := os.WriteFile(filepath.Join(i.unitDir(), name), []byte(unit), 0o644); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(i.snippetPath()), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(i.snippetPath(), []byte(i.snippet()), 0o644); err != nil {
		return err
	}
	conf := i.tmuxConf()
	data, err := os.ReadFile(conf)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if text := string(data); !slices.Contains(strings.Split(text, "\n"), i.sourceLine()) {
		if text != "" && !strings.HasSuffix(text, "\n") {
			text += "\n"
		}
		if err := os.WriteFile(conf, []byte(text+i.sourceLine()+"\n"), 0o644); err != nil {
			return err
		}
	}
	if err := i.run("systemctl", "--user", "daemon-reload"); err != nil {
		return err
	}
	if err := i.run("systemctl", append([]string{"--user", "enable", "--now"}, timers...)...); err != nil {
		return err
	}
	i.run("tmux", "source-file", i.snippetPath()) // shows the counter now if tmux runs; a new server reads the config
	return nil
}

func (i *installer) uninstall() error {
	if _, err := os.Stat(filepath.Join(i.unitDir(), timers[0])); err == nil {
		if err := i.run("systemctl", append([]string{"--user", "disable", "--now"}, timers...)...); err != nil {
			return err
		}
	}
	for name := range i.units() {
		if err := os.Remove(filepath.Join(i.unitDir(), name)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if err := os.Remove(i.snippetPath()); err != nil && !os.IsNotExist(err) {
		return err
	}
	os.Remove(filepath.Dir(i.snippetPath())) // only if nothing else is in it
	for _, conf := range []string{filepath.Join(i.home, ".tmux.conf"), filepath.Join(i.configHome, "tmux", "tmux.conf")} {
		data, err := os.ReadFile(conf)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		lines := strings.SplitAfter(string(data), "\n")
		kept := slices.DeleteFunc(slices.Clone(lines), func(line string) bool { return strings.TrimSpace(line) == i.sourceLine() })
		switch text := strings.Join(kept, ""); {
		case len(kept) == len(lines):
		case strings.TrimSpace(text) == "": // install created it
			if err := os.Remove(conf); err != nil {
				return err
			}
		default:
			if err := os.WriteFile(conf, []byte(text), 0o644); err != nil {
				return err
			}
		}
	}
	return i.run("systemctl", "--user", "daemon-reload")
}

// missing names the programs a timed run needs that the services' PATH does not hold: each run would fail on them.
func (i *installer) missing() []string {
	var missing []string
	for _, name := range []string{"process", "claude", "notify-send"} {
		found := false
		for _, dir := range filepath.SplitList(i.path) {
			if info, err := os.Stat(filepath.Join(dir, name)); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
				found = true
				break
			}
		}
		if !found {
			missing = append(missing, name)
		}
	}
	return missing
}
