// proc works the process approval queue: it reads the queue triage wrote to the state directory, records my
// decisions next to it, and starts the executor when I quit. It holds no credentials and calls no service API.
package main

import (
	"errors"
	"fmt"
	"os"
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

const usage = "usage: proc [count]"

// command runs one of proc's unattended subcommands; with none, proc is the TUI.
func command(root, name string, args []string) error {
	switch {
	case name == "count" && len(args) == 0:
		line, err := countLine(root, time.Now())
		if line != "" {
			fmt.Println(line)
		}
		return err
	}
	return errors.New(usage)
}
