package main

import (
	"context"
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
// under its profile's model and allowlist. Its output goes to a log under proc/runs. A run that fails puts every
// watermark back as it was, so the next run reads the same window again (process stores an item it already has only
// once), and the brief links the log. So does a run that hangs past runTimeout or is stopped (ctx), since a unit
// that never finishes would hold every later run. Either way, the urgent entries it queued notify.
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
	}
	cancel()
	if runErr == nil {
		runErr = phaseFailure(root)
	}
	if runErr != nil {
		fmt.Fprintf(log, "proc: the %s run failed: %v\n", mode, runErr)
	}
	if err := log.Close(); err != nil && runErr == nil {
		runErr = err
	}
	if runErr != nil {
		runErr = fmt.Errorf("the %s run failed (%w); log: %s", mode, runErr, logPath)
		if err := restoreWatermarks(root, marks); err != nil {
			runErr = errors.Join(runErr, err)
		}
		if err := noteInBrief(root, fmt.Sprintf("> ⚠ The %s run at %s failed; watermarks were left as they were, so the next run retries. Log: %s", mode, now.Local().Format("15:04"), logPath)); err != nil {
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

// phaseFailure reports a phase that failed although `process run` itself finished: process writes each one into the
// brief as a "> ⚠" line under its project.
func phaseFailure(root string) error {
	data, err := os.ReadFile(filepath.Join(root, "brief.md"))
	if err != nil {
		return err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if problem, ok := strings.CutPrefix(line, "> ⚠ "); ok {
			return errors.New(problem)
		}
	}
	return nil
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

// restoreWatermarks puts every watermarks file back as readWatermarks found it, removing any the run created.
func restoreWatermarks(root string, marks map[string][]byte) error {
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
