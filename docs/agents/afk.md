+++
base = "main"
bootstrap = ["test ! -f go.mod || go mod download"]
default_type = "go"

[task_types.go]
model = "opus"
effort = "high"
skill = "/tdd"

[task_types.docs]
model = "sonnet"
effort = "medium"
+++

# AFK

proc is a Go TUI over the state directory that claude-skills' process skills (`/brief`, triage, executor) write, plus the systemd user timers and the tmux/prompt counter that run them unattended. It never holds credentials or calls service APIs; everything it knows comes from the state directory.

The bootstrap is guarded (`test ! -f go.mod || …`) only because the repo started empty; once `go.mod` exists it simply downloads modules.

## Pre-PR skill

Run `/pre-pr` in the prepr phase, and again after any merge from `main`. It runs gofmt, `go vet`, `go test` and `go build`. Until the repo has a `go.mod`, those fail on `main` too: that is pre-existing, not the branch's fault. When CI is added, update `/pre-pr` to mirror it.

## Dev server

None. What parallel workers could collide on is the state directory (queue, decisions, ledger, profiles). Point proc at a per-slot directory during verification, `/tmp/afk-state-$AFK_SLOT`, seeded from fixtures, and never at the real `~/.local/state`.

Set `XDG_STATE_HOME` **on the command only**, never `export` it in your shell: `afk report` reads the same variable to find this worker's status, so an exported override hides your reports from the watcher. Remove the directory when you are done: `rm -rf /tmp/afk-state-$AFK_SLOT`.

## Verification recipes

1. **Behavior tests.** `go test ./...`. TUI behavior is tested by driving the model with key sequences over a fixture queue and asserting on the decisions file it writes, not on rendered strings alone.
2. **The real binary.** Build it (`go build -o /tmp/proc-$AFK_SLOT .` or the repo's build command) and drive it in a throwaway tmux session:
   `tmux new-session -d -s proc-verify-$AFK_SLOT -x 120 -y 40 "XDG_STATE_HOME=/tmp/afk-state-$AFK_SLOT /tmp/proc-$AFK_SLOT"`,
   then `tmux send-keys -t proc-verify-$AFK_SLOT <keys>` and `tmux capture-pane -p -t proc-verify-$AFK_SLOT` to check what it shows. For `$EDITOR` flows, set `EDITOR` on the command to a script that edits the file non-interactively. Kill the session when done: `tmux kill-session -t proc-verify-$AFK_SLOT`.
3. **systemd units and the counter.** Never install timers or units into the real user systemd (`~/.config/systemd/user`, `systemctl --user enable/start`): they would start running real sweeps. Check unit files with `systemd-analyze verify <unit>` and schedules with `systemd-analyze calendar '<OnCalendar>'` (America/Chicago). Test install/uninstall and their idempotence against a temporary target directory, and the counter against a fixture queue.
4. **Live runs are the human's.** Anything that needs real accounts, real timers firing, or `notify-send` on the desktop: verify what you can with fixtures, then list the exact commands and what to look for under **Manual verification** in the PR description, and say in your `afk report done` message that a live check is still pending.
