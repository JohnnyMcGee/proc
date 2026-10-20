package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// sweep is one unattended run of `process run`: every active profile's sweepers then triage, each phase headless
// under its profile's model and allowlist. Its output goes to a log under proc/runs, and the brief links the log when
// the run fails. A run that fails outright, hangs past runTimeout or is stopped (ctx) puts every watermark back as it
// was, so the next run reads the same window again (process stores an item it already has only once); a unit that
// never finished would hold every later run. A run that finishes with failed phases puts back only the watermarks of
// the sweeps that failed: a problem elsewhere (triage, the executor) is not cured by re-reading, and would otherwise
// pin every watermark until someone fixed it. Either way, the urgent entries it queued notify.
func sweep(ctx context.Context, root string, delta bool, now time.Time, process func(ctx context.Context, args []string, log io.Writer) error, send func(title, body string) error) error {
	mode, args := "full", []string{"run"}
	if delta {
		mode, args = "delta", append(args, "--delta")
	}
	logPath := filepath.Join(root, "proc", "runs", strings.ReplaceAll(stamp(now), ":", "-")+"-"+mode+".log")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return err
	}
	// Timers that missed their runs while the machine was off all fire at boot: one run at a time.
	lock, err := os.OpenFile(filepath.Join(root, "proc", "sweep.lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	if ctx.Err() != nil {
		return fmt.Errorf("the %s run was stopped before it started", mode)
	}
	marks, err := readWatermarks(root)
	if err != nil {
		return err
	}
	log, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	run, cancel := context.WithTimeout(ctx, runTimeout)
	runErr := process(run, args, log)
	if errors.Is(run.Err(), context.DeadlineExceeded) {
		runErr = fmt.Errorf("timed out after %s", runTimeout)
	} else if ctx.Err() != nil {
		runErr = errors.New("stopped (systemctl stop or shutdown)")
	}
	cancel()
	restore := map[string][]string(nil) // nil: every watermark
	if runErr == nil {
		restore, runErr = phaseFailure(root)
	}
	if runErr != nil {
		fmt.Fprintf(log, "proc: the %s run failed: %v\n", mode, runErr)
	}
	if err := log.Close(); err != nil && runErr == nil {
		runErr = err
	}
	if runErr != nil {
		runErr = fmt.Errorf("the %s run failed (%w); log: %s", mode, runErr, logPath)
		if err := restoreWatermarks(root, marks, restore); err != nil {
			runErr = errors.Join(runErr, err)
		}
		left := "every watermark was left as it was, so the next run retries"
		if restore != nil {
			left = "the watermark of each failed sweep was left as it was, so it retries next run"
		}
		if err := noteInBrief(root, fmt.Sprintf("> ⚠ The %s run at %s failed; %s. Log: %s", mode, now.Local().Format("15:04"), left, logPath)); err != nil {
			runErr = errors.Join(runErr, err)
		}
	}
	return errors.Join(runErr, notifyUrgent(root, now, send))
}

// runTimeout bounds one run. A full run of every profile takes minutes; one still going after this is stuck.
var runTimeout = time.Hour

// runProcess runs claude-skills' process CLI on the state directory proc reads, with its output in log. Stopping it
// stops its headless claude phases too.
func runProcess(ctx context.Context, args []string, log io.Writer) error {
	cmd := exec.CommandContext(ctx, "process", args...)
	cmd.Stdout, cmd.Stderr = log, log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
	cmd.WaitDelay = time.Minute // then SIGKILL
	return cmd.Run()
}

// phaseFailure reports the phases that failed although `process run` itself finished: process writes each one into
// the brief as a "> ⚠" line under its project's "## <profile>" heading. It returns the sources whose sweep failed, by
// profile; "sweep-<source> ..." is how process names a sweep's problem.
func phaseFailure(root string) (map[string][]string, error) {
	data, err := os.ReadFile(filepath.Join(root, "brief.md"))
	if err != nil {
		return nil, err
	}
	var problems []error
	sweeps, profile := map[string][]string{}, ""
	for _, line := range strings.Split(string(data), "\n") {
		if name, ok := strings.CutPrefix(line, "## "); ok {
			profile = name
		} else if problem, ok := strings.CutPrefix(line, "> ⚠ "); ok {
			problems = append(problems, errors.New(problem))
			if phase, _, _ := strings.Cut(problem, " "); strings.HasPrefix(phase, "sweep-") && profile != "" {
				sweeps[profile] = append(sweeps[profile], strings.TrimPrefix(phase, "sweep-"))
			}
		}
	}
	return sweeps, errors.Join(problems...)
}

// readWatermarks returns every profile's watermarks file, by path, as it is now.
func readWatermarks(root string) (map[string][]byte, error) {
	paths, err := filepath.Glob(filepath.Join(root, "profiles", "*", "watermarks.json"))
	if err != nil {
		return nil, err
	}
	marks := map[string][]byte{}
	for _, path := range paths {
		if marks[path], err = os.ReadFile(path); err != nil {
			return nil, err
		}
	}
	return marks, nil
}

// restoreWatermarks puts the watermarks of the given sources back, by profile, as readWatermarks found them. With no
// sources it puts every watermarks file back, removing any the run created.
func restoreWatermarks(root string, marks map[string][]byte, sources map[string][]string) error {
	if sources != nil {
		for profile, names := range sources {
			if err := restoreSources(filepath.Join(root, "profiles", profile, "watermarks.json"), marks, names); err != nil {
				return err
			}
		}
		return nil
	}
	now, err := readWatermarks(root)
	if err != nil {
		return err
	}
	for path := range now {
		if _, before := marks[path]; !before {
			if err := os.Remove(path); err != nil {
				return err
			}
		}
	}
	for path, data := range marks {
		if err := os.WriteFile(path, data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// restoreSources puts some sources' watermarks in one watermarks file back, leaving the others as the run left them.
func restoreSources(path string, marks map[string][]byte, sources []string) error {
	before, after := map[string]any{}, map[string]any{}
	if data, ok := marks[path]; ok {
		if err := json.Unmarshal(data, &before); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil // the run stored nothing for this profile
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, &after); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	for _, source := range sources {
		if mark, ok := before[source]; ok {
			after[source] = mark
		} else {
			delete(after, source)
		}
	}
	if data, err = json.MarshalIndent(after, "", "  "); err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

// noteInBrief puts a line at the top of the current brief, under its title.
func noteInBrief(root, note string) error {
	path := filepath.Join(root, "brief.md")
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	brief := string(data)
	if title, rest, ok := strings.Cut(brief, "\n"); ok && strings.HasPrefix(title, "# ") {
		brief = title + "\n\n" + note + "\n" + rest
	} else {
		brief = note + "\n\n" + brief
	}
	return os.WriteFile(path, []byte(brief), 0o644)
}
