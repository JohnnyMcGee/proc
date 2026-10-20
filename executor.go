package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

// defaultExecutor is claude-skills' executor: it carries out approved decisions as headless Claude under the
// profile's executor allowlist, and appends a result per entry. PROC_EXECUTOR replaces it.
const defaultExecutor = "process execute"

// startExecutor launches the executor in the background on the state directory proc showed, detached from the
// terminal so it outlives proc, with its output in executor.log. It returns the log's path.
func startExecutor(root string) (string, error) {
	command := os.Getenv("PROC_EXECUTOR")
	if command == "" {
		command = defaultExecutor
	}
	logPath := filepath.Join(root, "executor.log")
	log, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return "", err
	}
	defer log.Close()
	cmd := exec.Command("sh", "-c", command)
	cmd.Env = append(os.Environ(), "XDG_STATE_HOME="+filepath.Dir(root))
	cmd.Stdout, cmd.Stderr = log, log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return "", err
	}
	return logPath, cmd.Process.Release()
}
