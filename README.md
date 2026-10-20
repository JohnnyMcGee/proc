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
| `e` | edit the draft in `$EDITOR`, then approve the edited text, with an optional one-line reason |
| `s` | skip, with an optional one-line reason |
| `p` | approve, and make this action type silent for the project from now on |
| `q` | quit, starting the executor in the background if there is work for it |

A decision is final: an entry you decided is the executor's.

## The state directory

proc reads and writes only `$XDG_STATE_HOME/process/` (default `~/.local/state/process/`), which claude-skills' process pipeline owns. It holds no credentials and calls no service API.

- reads `queue.jsonl` (triage's queue entries; the last line per id wins), `profiles/<project>/items.jsonl` (the context of each entry's items) and `results.jsonl` (the executor's outcome per entry);
- appends `decisions.jsonl`, one decision per entry, each validated against the shared schema first;
- writes a draft under `proc/` while `$EDITOR` has it open, and the executor's output to `executor.log`.

Every record follows claude-skills' `process/schema.json`, vendored unchanged as `schema/schema.json`. When it changes there, copy it over.

On quit, proc runs the executor as `process execute` (claude-skills' process CLI), detached, with `XDG_STATE_HOME` set to the directory proc showed. Set `PROC_EXECUTOR` to run another command instead.

## Development

`go test ./...` drives the TUI with key sequences over the fixture queue in `testdata/state` and checks the decisions it writes. CI runs gofmt, `go vet`, `go test` and `go build` on every PR.
