# ADR-0008: Twirp-style admin API and the Java server SDK

- Status: accepted
- Date: 2026-11-04
- Deciders: Tessera maintainers

## Context

The primary integration target is Java backends. They need to mint access tokens, manage
rooms and participants, control recordings and receive webhooks, with as few dependencies as
possible (dependency conflicts are the number one pain with server SDKs in Java shops).

## Decision

### Admin API on the server

1. Services are defined in `proto/tessera/v1/admin.proto` (`RoomService`) and
   `recording.proto` (`RecordingService`, `RecorderWorkerService`).
2. Transport follows the **Twirp v7 wire protocol**, implemented from scratch in
   `pkg/admin` (no generator plugin, no runtime dependency):
   `POST /twirp/tessera.v1.<Service>/<Method>` with either
   `Content-Type: application/protobuf` (binary) or `application/json` (protojson,
   `UseProtoNames=false`, i.e. lowerCamelCase field names, `EmitUnpopulated=false`; servers accept
   both camelCase and snake_case on input).
3. Errors use the Twirp error JSON `{"code": "...", "msg": "...", "meta": {...}}` with Twirp
   codes mapped to HTTP status (`not_found`→404, `invalid_argument`→400,
   `permission_denied`→403, `unauthenticated`→401, `already_exists`→409,
   `resource_exhausted`→429, `unimplemented`→501, `unavailable`→503, `internal`→500).
4. Auth: `Authorization: Bearer <jwt>` with admin grants (ADR-0004).

### Webhooks

5. The server POSTs `tessera.v1.WebhookEvent` to configured URLs. Encoding per endpoint:
   `json` (default, protojson) or `protobuf`. Headers:
   - `Tessera-Event-Id: <uuid>` (idempotency key), `Tessera-Event-Type: <type>`
   - `Tessera-Key: <api key>` (selects the secret)
   - `Tessera-Signature: t=<unix seconds>,v1=<hex(HMAC-SHA256(secret, "<t>.<raw body>"))>`
   Receivers must check the signature in constant time and reject `|now - t| > 300 s`.
   Delivery is at-least-once with exponential backoff (1 s → 5 min, 10 attempts), per-URL
   ordered queue, bounded memory (oldest dropped, counted in `num_dropped`).

### Java SDK (`sdk/java`)

6. Java **17** baseline, Maven, artifact `io.github.keshavgoyal26:tessera-server-sdk`,
   root package `io.github.keshavgoyal26.tessera`.
7. **Runtime dependencies: `com.google.protobuf:protobuf-java` only.** HTTP uses
   `java.net.http.HttpClient`; JWT uses `javax.crypto.Mac`; the SDK speaks the binary
   protobuf flavour of the admin API. JSON webhook bodies are parsed with
   `protobuf-java-util`'s `JsonFormat` declared as an **optional** dependency: if the
   application wants JSON webhooks it adds `protobuf-java-util`; otherwise it configures its
   webhook endpoint for `protobuf` encoding. (Jackson is deliberately not used: the generated
   protobuf classes already give typed models.)
8. Java classes are **generated at build time** from `../../proto` by
   `io.github.ascopes:protobuf-maven-plugin` (downloads `protoc` from Maven Central), so the
   Java models can never drift from `proto/`. Generated package:
   `io.github.keshavgoyal26.tessera.proto.v1` (set with `java_package`).
9. Public API (stable, documented with Javadoc):
   - `AccessToken` builder (identity, name, metadata, attributes, TTL, `RoomGrant`) → `toJwt()`.
   - `RoomServiceClient`: create/list/delete rooms, update room metadata,
     list/get/remove participants, update participant, mute published track.
   - `RecordingServiceClient`: start/stop/list/get recordings.
   - `WebhookReceiver`: `receive(body, headers)` → verified `WebhookEvent`.
   - `TesseraException` hierarchy carrying the Twirp code.
   - Optional Spring Boot example (`examples/java-spring`), not part of the SDK.
10. **Contract tests** keep Java aligned with Go: golden fixtures in `testdata/contract/`
    (JWT claims, webhook bodies and signatures, Twirp request/response JSON and binary) are
    produced by Go tests and verified by Java tests (and vice versa for tokens).

## Consequences

- One dependency (`protobuf-java`) for most users; zero JSON library conflicts.
- Twirp is trivial to call from curl, any language, or a browser admin console.
- The SDK exposes protobuf message types for models; small convenience wrappers can be added
  later without breaking.

## Alternatives considered

- **gRPC**: heavy Java dependency tree (Netty/grpc-java), HTTP/2 requirements in proxies.
- **REST/OpenAPI with Jackson**: duplicated schema, drift risk, dependency conflicts.
- **Twirp with generated server code (twitchtv/twirp)**: adds a generator plugin and runtime;
  the protocol is small enough to implement in ~300 lines.
