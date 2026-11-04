# WP3: Admin API, webhooks and Java SDK

**Goal:** a complete Twirp-style admin API on the server, reliable signed webhooks, and a
dependency-light Java server SDK that stays aligned with `proto/` through contract tests.

## Owns (exclusive)

`pkg/admin`, `pkg/webhook`, `sdk/java/**`, `examples/java-spring/**`, `testdata/contract/**`
(new; golden fixtures), the `java` job in CI.

## Interfaces

- Consumes: `room.Admin` (WP1), `recorder.Service` and `recorder.WorkerAPI` (WP4),
  `auth.Verifier` / `auth.Claims` / `auth.RoomGrant` (WP1), `pkg/errs`.
  Until WP1/WP4 land, test against fakes implementing those interfaces.
- Provides: `admin.NewServer(admin.Params)` (signature frozen; mounted at `/twirp/` by
  `cmd/tessera`), `webhook.Notifier` / `webhook.NewNotifier` (frozen), `webhook.Sign` /
  `webhook.Verify`, a Go Twirp client helper `admin.Client` (for E2E, recorder worker and
  load tester), the Java SDK API in ADR-0008.

## Scope

1. **Twirp server** (`pkg/admin`): routing for the three services, `application/protobuf` and
   `application/json` (protojson, accepts camelCase and snake_case), body size limit, Twirp
   error JSON and HTTP status mapping from `pkg/errs`, request metrics and logging.
2. **AuthZ**: bearer token verification, per-RPC grant checks (`room_create`, `room_list`,
   `room_admin` scoped to `grants.room` when set, `room_record`, `recorder_worker`).
3. **Go client helper** `admin.Client` (protobuf over HTTP) with typed methods for all three
   services.
4. **Webhooks** (`pkg/webhook`): per-endpoint ordered queue (bounded, drop-oldest with
   `num_dropped`), JSON/protobuf encoding, signing headers, retries with exponential backoff
   (1 s → 5 min, 10 attempts), timeouts, graceful `Close` flush, metrics.
5. **Java SDK**: finish and harden (retry policy for idempotent calls, `room_config` grant,
   Javadoc, `module-info` or Automatic-Module-Name), publish setup (sources/javadoc jars,
   signing profile not enabled by default).
6. **Contract tests**: golden fixtures produced by Go (`go test ./pkg/admin -run Golden
   -update`) under `testdata/contract/`:
   - `tokens/`: JWT claims JSON for representative grants; Java must produce identical claim
     JSON for the same inputs (compare parsed JSON, not bytes) and Go must verify
     Java-minted tokens (Java test writes tokens, Go test reads them, or a shared vector
     with fixed `nbf/exp`).
   - `webhook/`: event JSON + protobuf bodies with headers and signatures; Java
     `WebhookReceiver` must verify and decode every fixture; Go `webhook.Verify` too.
   - `twirp/`: request/response JSON and binary pairs for each RPC; Java parses them with the
     generated classes; Go admin server round-trips them.
   - The webhook vector `t=1700000000,v1=b856...f163` already pins `HMAC("secret","1700000000.{}")`
     in both languages.
7. **Spring Boot example**: token endpoint, webhook controller, admin calls; built in CI as a
   separate step once the SDK is stable (optional job).

## Milestones

| ID | Deliverable |
|----|-------------|
| M1 | Twirp router + error mapping + authZ + `admin.Client`, all RoomService RPCs against a fake `room.Admin` |
| M2 | Webhook notifier with retries, signing, ordering, metrics |
| M3 | RecordingService + RecorderWorkerService routes against fakes; contract fixtures + Java/Go contract tests |
| M4 | Live tests against WP1 M1 (`cmd/tessera --dev`): Java SDK creates rooms, lists participants joined by a Go client |
| M5 | Java SDK polish, Spring example, release dry run |

## Acceptance criteria

- Every RPC in `admin.proto` and `recording.proto` reachable with both encodings, correct
  grants enforced (table test of RPC × grant), Twirp errors validated against the spec.
- Webhook delivery survives receiver downtime (retries), preserves order per endpoint, and
  never blocks room goroutines (`Notify` is O(1), non-blocking).
- `mvn -B verify` green; Java SDK has exactly one required runtime dependency
  (`protobuf-java`); contract tests run in both `go test` and `mvn verify`.

## Required tests

Go: router/codec/error tables, authZ table, webhook retry/backoff with `httptest` and fake
clock, queue overflow, signature vectors, golden fixture round-trips. Java: token claims
vs fixtures, webhook fixtures (JSON with protobuf-java-util on the test classpath, and
protobuf), Twirp client against an in-process HTTP server for every method, error mapping.
