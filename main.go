// proc works the process approval queue: it reads the queue triage wrote to the state directory, records my
// decisions next to it, and starts the executor when I quit. It holds no credentials and calls no service API.
package main

import (
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
	m, err := newModel(root, time.Now)
	if err != nil {
		return err
	}
	_, err = tea.NewProgram(m, tea.WithAltScreen()).Run()
	return err
}
