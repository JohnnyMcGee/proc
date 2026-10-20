---
name: pre-pr
description: Run proc's pre-PR checks — gofmt, go vet, go test and go build. Use before committing for review, pushing, or opening or updating a PR.
---

# Pre-PR

The repo's own gate before a PR goes up: the checks CI runs (`.github/workflows/ci.yml`), run locally so they don't bounce in CI.

## Steps

1. **Format:** `test -z "$(gofmt -l .)"`. If it fails, `gofmt -l .` lists the files; fix them with `gofmt -w <files>`.
2. **Vet:** `go vet ./...`.
3. **Tests:** `go test ./...`.
4. **Build:** `go build ./...`.

Fix what each check reports and re-run it until it passes. A failure you can prove is not caused by the diff — it also fails on the base branch — is pre-existing: report it and carry on.

Report plainly: each check passed, failed (with output) or skipped (with why). A failing check means the branch is not ready.

## Self-improvement (evidence-triggered — never asks)

After the run, compare what this skill told you to do with what happened. Edit this file only on concrete evidence it is stale or wrong:

- a command, path, flag or name here errored or no longer exists;
- you had to deviate from the steps to get a correct result;
- the user corrected or overrode a step;
- a needed step was missing and you improvised it.

Then:

1. Make one surgical edit fixing exactly that. No speculative improvements, no rewrites.
2. Append a dated Changelog bullet naming the change and the evidence.
3. Tell the user one line: `Improved /pre-pr: <before> → <after> (triggered by <evidence>).`

A clean run changes nothing, and you say nothing about self-improvement.

When CI's PR jobs change, change these steps to mirror them exactly.

## Changelog

- 2026-10-20 — Created by /afk-setup. No CI yet; checks are the standard Go gate (gofmt, vet, test, build).
- 2026-10-20 — The repo gained a `go.mod` and CI (`.github/workflows/ci.yml`); the steps already mirror its jobs. Dropped the no-`go.mod` caveat.
