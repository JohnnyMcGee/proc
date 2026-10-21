// proc works the process approval queue: it reads the queue triage wrote to the state directory, records my
// decisions next to it, and starts the executor when I quit. It holds no credentials and calls no service API.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "proc:", err)
		os.Exit(1)
	}
}

func run() error {
	root, err := stateRoot()
	if err != nil {
		return err
	}
	if len(os.Args) > 1 {
		return command(root, os.Args[1], os.Args[2:])
	}
	m, err := newModel(root, time.Now)
	if err != nil {
		return err
	}
	if _, err := tea.NewProgram(m, tea.WithAltScreen()).Run(); err != nil {
		return err
	}
	if m.executorErr != nil {
		return fmt.Errorf("could not start the executor (%w); your decisions are saved for its next run", m.executorErr)
	}
	if m.executorLog != "" {
		fmt.Println("proc: executor started in the background; log:", m.executorLog)
	}
	return nil
}

const usage = "usage: proc [init | count | notify | sweep [--delta] | install | uninstall]"

// command runs one of proc's unattended subcommands; with none, proc is the TUI.
func command(root, name string, args []string) error {
	switch {
	case name == "count" && len(args) == 0:
		line, err := countLine(root, time.Now())
		if line != "" {
			fmt.Println(line)
		}
		return err
	case name == "notify" && len(args) == 0:
		return notifyUrgent(root, time.Now(), notifySend)
	case name == "sweep" && (len(args) == 0 || len(args) == 1 && args[0] == "--delta"):
		// systemd stops a unit, and shutdown stops every unit, with SIGTERM: let the run fail the way it should.
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
		defer stop()
		return sweep(ctx, root, len(args) == 1, time.Now(), runProcess, notifySend)
	case name == "init" && len(args) == 0:
		i, err := newInitializer()
		if err != nil {
			return err
		}
		return i.init()
	case name == "install" && len(args) == 0:
		i, err := newInstaller()
		if err != nil {
			return err
		}
		if err := i.install(); err != nil {
			return err
		}
		fmt.Println("proc: sweeps run at 08:00 (full), 12:30 and 16:30 (delta), America/Chicago; logs in", filepath.Join(root, "proc", "runs"))
		if missing := i.missing(); len(missing) > 0 {
			fmt.Printf("proc: warning: %s not on PATH, so every timed run will fail; install it, then run proc install again\n", strings.Join(missing, ", "))
		}
		return nil
	case name == "uninstall" && len(args) == 0:
		i, err := newInstaller()
		if err != nil {
			return err
		}
		if err := i.uninstall(); err != nil {
			return err
		}
		fmt.Println("proc: timers and counter removed; a running tmux shows the counter until it restarts")
		return nil
	}
	return errors.New(usage)
}
