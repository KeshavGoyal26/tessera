# Work packages

Phase 0 produced the contracts (proto, Go interfaces, SDK skeletons, CI). Four sessions now
work **in parallel**; WP5 starts when WP1 milestone M3 is done.

| WP | Title | Owns (exclusive) | Depends on |
|----|-------|------------------|-----------|
| [WP1](WP1-sfu-core.md) | SFU core | `pkg/rtc/**`, `pkg/room`, `pkg/signal`, `pkg/auth`, `pkg/config`, `pkg/client`, `cmd/tessera`, `test/e2e/**` | — |
| [WP2](WP2-browser-sdk.md) | Browser SDK and demo | `sdk/js/**` (except `src/gen`), `examples/web-demo/**` | WP1 M2 for E2E |
| [WP3](WP3-admin-webhooks-java.md) | Admin API, webhooks, Java SDK | `pkg/admin`, `pkg/webhook`, `sdk/java/**`, `examples/java-spring/**`, `testdata/contract/**` | WP1 `room.Admin` impl for live tests (mocks before) |
| [WP4](WP4-hls-recorder.md) | HLS recorder | `pkg/hls`, `pkg/recorder`, `pkg/storage`, `cmd/tessera-recorder`, `testdata/rtp/**` | WP1 `pkg/client` (M2) for live jobs (fixtures before) |
| [WP5](WP5-integration-performance.md) | Integration, load, performance | `cmd/tessera-loadtest`, `deploy/**`, `pkg/metrics`, `bench/**`, `docs/perf/**` | WP1 M3, WP4 M3 |

## Rules for every session (from ADR-0011)

1. Edit only directories you own, your own WP file and your own row/section in
   `docs/PROGRESS.md`. Never edit another WP's files, even to "fix" something: open the
   change as a note in PROGRESS ("Requests" section) and ping the owner.
2. **Shared files**:
   - `proto/**` (+ `gen/go/**`, `sdk/js/src/gen/**`): additive only, one dedicated commit
     `feat(proto): <what>` containing the `.proto` change and **both** regenerated outputs
     (`make proto`). `git pull --rebase` right before. Log it in PROGRESS "Proto changes".
   - `go.mod`/`go.sum`: add what you need; on conflict `git pull --rebase`, re-run
     `go mod tidy`, never merge commits.
   - `pkg/errs`, `pkg/version`: frozen; additive changes in a dedicated commit.
   - `.github/workflows/ci.yml`: edit only your job; anything else in a `ci:` commit.
   - `docs/ARCHITECTURE.md`, `docs/PROTOCOL.md`, `docs/adr/**`: `docs:` commits; changing a
     decision means a new ADR.
3. Cross-WP interfaces (`room.Admin`, `signal.SessionHandler`, `client.Client`,
   `recorder.Service`, `recorder.WorkerAPI`, `recorder.RTPSource`, `webhook.Notifier`,
   `auth.Verifier`/`auth.KeyProvider`, `storage.Storage`, `hls.Sink`) may only be extended
   additively by their owner. Consumers code against interfaces and use fakes in tests.
4. Constructors called from `cmd/tessera/main.go` keep their Phase 0 signatures
   (`room.NewManager`, `rtc.NewEngine`, `webhook.NewNotifier`, `recorder.NewCoordinator`,
   `admin.NewServer`, `signal.NewServer`). Need a new dependency? Add a field to the
   `Params` struct (zero value must keep working) and ask WP1 to wire it.
5. Small conventional commits (`feat(rtc): ...`, `test(hls): ...`), `git pull --rebase`
   before each push, CI green on every push. Whoever turns `main` red fixes it first.
6. Every exported identifier is documented. Hot-path code (`pkg/rtc/**`) ships with
   benchmarks proving `0 allocs/op`.
7. Update your milestone checkboxes in `docs/PROGRESS.md` as you finish them.

## Toolchain

Go 1.27.3 (`go.mod` toolchain line; `GOTOOLCHAIN=auto` downloads it), golangci-lint v2.15.0,
buf 1.75.0 + protoc-gen-go v1.36.12, Node 22 + npm, JDK 17+ and Maven 3.9+, FFmpeg/ffprobe
for WP4 tests (skip with a clear message when missing; CI installs them in the WP4 job).
