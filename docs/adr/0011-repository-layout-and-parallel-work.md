# ADR-0011: Monorepo layout and ownership rules for parallel work

- Status: accepted
- Date: 2026-11-04
- Deciders: Tessera maintainers

## Context

After Phase 0, four work packages (WP1 to WP4) run in parallel sessions pushing to the same
repository, with WP5 following. They must not edit each other's files.

## Decision

1. One monorepo, one Go module `github.com/KeshavGoyal26/tessera` at the root (so `pkg/client`
   can be used by the recorder and load tester without versioning dances).
2. **Exclusive ownership** of directories per work package, listed in
   `docs/workpackages/WPn-*.md`. A WP edits only its own directories plus its own work
   package file and its own section of `docs/PROGRESS.md`.
3. **Shared, frozen-by-default files** and their change rules:

   | Path | Rule |
   |------|------|
   | `proto/**`, `gen/go/**`, `sdk/js/src/gen/**`, `buf.yaml`, `buf.gen.yaml` | Change only in a **small dedicated commit** `feat(proto): ...` that contains the `.proto` change *and* the regenerated Go and TS code, nothing else. Additive changes only (new fields/messages/RPCs); `buf breaking` must pass. Announce the change in `docs/PROGRESS.md` ("Proto changes" log). Pull/rebase immediately before such a commit. |
   | `go.mod`, `go.sum` | Any WP may add a dependency it needs. Conflicts are resolved by **rebasing** onto `main` and running `go mod tidy`; never merge-commit. Do not bump versions of deps you do not own without noting it in PROGRESS. |
   | `pkg/errs` | Frozen contract (error sentinels). Additive changes via a dedicated `feat(errs): ...` commit. |
   | `.github/workflows/ci.yml` | Each WP may only edit its own job; other edits via a dedicated `ci: ...` commit. |
   | `docs/ARCHITECTURE.md`, `docs/PROTOCOL.md`, `docs/adr/**` | Edits in dedicated `docs: ...` commits; changing a decision needs a new ADR. |
   | `testdata/contract/**` | Owned by WP3; other WPs may add files under their own subdirectory. |
   | `README.md`, `docs/PROGRESS.md` | Small edits allowed to everyone; rebase on conflict. |

4. **Interfaces are the contract.** Interfaces created in Phase 0 that are consumed across
   WPs (`room.Admin`, `recorder.Service`, `recorder.WorkerAPI`, `webhook.Notifier`,
   `client.Client`, `auth.Verifier`, `storage.Storage`, `signal.SessionHandler`) may be
   extended additively by their owner; breaking changes need agreement recorded in PROGRESS.
5. **Wiring** (`cmd/tessera/main.go`) is done in Phase 0 and owned by WP1. WP3/WP4 never
   edit it; their constructors keep the signatures declared in Phase 0.
6. Workflow: small conventional commits pushed to `main` (`feat(rtc): ...`, `fix(hls): ...`,
   `test(admin): ...`). Always `git pull --rebase` before pushing. CI must be green on every
   push; a red main is fixed by whoever broke it before anything else.

## Consequences

- Parallel sessions can work without merge conflicts except in `go.mod`/`go.sum` and
  `PROGRESS.md`, which rebase cleanly.
- Cross-WP features (for example a new signal message) require a coordinated proto commit
  first, then implementation in each WP.
