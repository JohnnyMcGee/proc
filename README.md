proc is a Go TUI that serves as a minimalist portal to navigate my work. It is a front-end for the /brief skill from claude-skills. Its philosophy is to remove every manual process from my day, so I can just solve problems.

## Build

```bash
go build        # writes ./proc
go install .    # or put it on PATH as ~/go/bin/proc
```

## Working the queue

`proc` lists the entries waiting on you in the approval queue, grouped by project and then by urgency (`!` is urgent). FYI and superseded entries stay out; entries you decided stay for a week with their result.

| Key | Does |
| --- | --- |
| `j` / `k`, arrows | move (scroll when an entry is open) |
| `enter` / `esc` | open the entry (its draft, action and the context of every item it merged) / back to the list |
| `a` | approve |
| `e` | edit the draft in `$EDITOR`, then approve the edited text, with an optional one-line reason (`esc` there keeps the edit for the next `e`) |
| `s` | skip, with an optional one-line reason |
| `p` | approve, and make this action type silent for the project from now on |
| `q` | quit, starting the executor in the background if there is work for it |
| `ctrl+c` | quit without starting the executor; your decisions stay saved for its next run |

A decision is final: an entry you decided, or one triage approved silently, is the executor's.

## Running without me

```bash
proc install      # timers + tmux counter; run it again after moving proc or changing PATH
proc uninstall    # removes both; safe to run twice
```

`proc install` writes systemd user timers to `~/.config/systemd/user/` and enables them. They run claude-skills' `process run` through `proc sweep`: a full sweep at 08:00, and delta sweeps (`--delta`) at 12:30 and 16:30, America/Chicago. Each phase runs as headless Claude under the model and tool allowlist its profile sets. The timers are persistent, so a run missed while the machine was off fires when it comes back. The services get the `PATH`, `XDG_STATE_HOME` and `XDG_CONFIG_HOME` of the shell that ran `proc install`, so they find `process`, `claude`, `notify-send`, the state directory and the profiles the way it did; install warns about any of them it cannot find there. User timers run while the user's systemd manager runs, which on a desktop means while I'm logged in (`loginctl enable-linger` keeps them running without a login).

- **`proc sweep [--delta]`** is one run. It writes the run's output to `proc/runs/<time>-<full|delta>.log`. Runs wait for each other, so the boot-time catch-up never runs two at once. When `process run` fails, is still going after an hour, or is stopped (`systemctl --user stop`, a shutdown), proc stops everything it started and puts every profile's watermarks back as they were before the run, so the next run reads the same window again. When it finishes with failed phases (`⚠` lines in the brief), proc puts back only the watermark of each sweep that failed: re-reading cannot fix a triage or executor problem. Either way it adds a `⚠` line under the brief's title with the path of the log. Watch it with `systemctl --user status proc-brief proc-delta` and `journalctl --user -u proc-brief`.
- **`proc notify`**, run after every sweep, sends `notify-send` for each urgent entry from the last day still waiting on me (pending, or FYI), once per entry; an older one is in the brief already. Sent entries are recorded in `proc/notified.jsonl`, and a send that fails is retried next time. Triage decides what is urgent from the profile's `[urgency]` rules (my manager, incident channels, deploy-blocking reviews); proc only delivers it.
- **`proc count`** prints the counter: `proc 3 !1` is 3 entries waiting on a decision, 1 of them urgent. It prints nothing when nothing waits. `proc install` adds it to tmux's `status-right` through `~/.config/proc/proc.tmux.conf`, sourced from your tmux config, and widens `status-right-length` to fit it. After moving proc and running `proc install` again, restart tmux to pick up the new path. For a shell prompt, put `$(proc count)` in `PS1`.

## The state directory

Apart from what `proc install` puts in place, proc reads and writes only `$XDG_STATE_HOME/process/` (default `~/.local/state/process/`), which claude-skills' process pipeline owns. It holds no credentials and calls no service API: `proc sweep` leaves that to `process run`.

- reads `queue.jsonl` (triage's queue entries; the last line per id wins), `profiles/<project>/items.jsonl` (the context of each entry's items) and `results.jsonl` (the executor's outcome per entry);
- appends `decisions.jsonl`, one decision per entry, each validated against the shared schema first;
- writes a draft under `proc/` while `$EDITOR` has it open, and the executor's output to `executor.log`;
- `proc sweep` restores `profiles/<project>/watermarks.json` after a failed run, writes run logs under `proc/runs/` and adds a line to `brief.md` when a run fails; `proc notify` records what it sent in `proc/notified.jsonl`.

Every record follows claude-skills' `process/schema.json`, vendored unchanged as `schema/schema.json`. When it changes there, copy it over.

On quit, proc runs the executor as `process execute` (claude-skills' process CLI), detached, with `XDG_STATE_HOME` set to the directory proc showed. Set `PROC_EXECUTOR` to run another program and its arguments instead. proc looks the program up before quitting, and says so if it is not installed.

## Development

`go test ./...` drives the TUI with key sequences over the fixture queue in `testdata/state` and checks the decisions it writes. CI runs gofmt, `go vet`, `go test` and `go build` on every PR.
