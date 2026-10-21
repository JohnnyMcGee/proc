package main

import (
	"fmt"
	"time"
)

// countLine is the tmux status / shell prompt segment: how many entries wait on me, and how many of those are
// urgent. It is empty when nothing waits, so the segment disappears.
func countLine(root string, now time.Time) (string, error) {
	rows, err := loadRows(root, now)
	if err != nil {
		return "", err
	}
	pending, urgent := 0, 0
	for _, r := range rows {
		if r.Decision == nil && r.Status == "pending" {
			pending++
			if r.Urgency == "urgent" {
				urgent++
			}
		}
	}
	switch {
	case pending == 0:
		return "", nil
	case urgent == 0:
		return fmt.Sprintf("proc %d", pending), nil
	}
	return fmt.Sprintf("proc %d !%d", pending, urgent), nil
}
