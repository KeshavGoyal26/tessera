# Tessera Server SDK for Java

Server-side SDK for [Tessera](../../README.md): mint access tokens, manage rooms and
participants, control recordings and verify webhooks from a Java 17+ backend.

> Status: Phase 0 scaffolding (`0.1.0-SNAPSHOT`). The public API below is in place and tested
> against a local stub server; it is finalized together with the server admin API in WP3.
> Design: [ADR-0004](../../docs/adr/0004-api-keys-and-jwt-access-tokens.md) (tokens) and
> [ADR-0008](../../docs/adr/0008-admin-api-and-java-sdk.md) (admin API, webhooks, SDK).

## Install

Not yet published to Maven Central. Install locally with `mvn -B install` in this directory, then:

```xml
<dependency>
  <groupId>io.github.keshavgoyal26</groupId>
  <artifactId>tessera-server-sdk</artifactId>
  <version>0.1.0-SNAPSHOT</version>
</dependency>

<!-- Only if your webhook endpoints use the default JSON encoding: -->
<dependency>
  <groupId>com.google.protobuf</groupId>
  <artifactId>protobuf-java-util</artifactId>
  <version>4.36.2</version>
</dependency>
```

Gradle: `implementation("io.github.keshavgoyal26:tessera-server-sdk:0.1.0-SNAPSHOT")`.

## Usage

### Access tokens

```java
import io.github.keshavgoyal26.tessera.AccessToken;
import io.github.keshavgoyal26.tessera.RoomGrant;

String jwt = AccessToken.builder()
    .apiKey("APIxxxxxxxx")
    .secret(System.getenv("TESSERA_API_SECRET"))   // at least 32 bytes
    .identity("alice")                              // required for room_join
    .name("Alice")
    .metadata("{\"userId\":42}")
    .attribute("role", "host")
    .ttl(Duration.ofHours(1))                       // default 6 h, max 24 h for room_join
    .grants(RoomGrant.builder()
        .room("standup")
        .roomJoin(true)
        .canPublishSources(List.of(RoomGrant.SOURCE_CAMERA, RoomGrant.SOURCE_MICROPHONE))
        .build())
    .toJwt();
```

Tokens are HS256 JWTs. Only non-default grant fields are written; `canPublish`,
`canSubscribe` and `canPublishData` are nullable (`null` = server default, `true`). Secrets
shorter than 32 bytes are rejected unless `allowShortSecret(true)` is set (development servers
only).

### Rooms and participants

```java
import io.github.keshavgoyal26.tessera.RoomServiceClient;
import io.github.keshavgoyal26.tessera.proto.v1.*;

RoomServiceClient rooms = RoomServiceClient.create(
    "https://tessera.example.com", "APIxxxxxxxx", System.getenv("TESSERA_API_SECRET"));

Room room = rooms.createRoom(CreateRoomRequest.newBuilder()
    .setName("standup")
    .setMaxParticipants(10)
    .build()).getRoom();

List<ParticipantInfo> people = rooms.listParticipants(
    ListParticipantsRequest.newBuilder().setRoom("standup").build()).getParticipantsList();

rooms.mutePublishedTrack(MutePublishedTrackRequest.newBuilder()
    .setRoom("standup").setIdentity("bob").setTrackSid("TR_123").setMuted(true).build());

rooms.removeParticipant(RemoveParticipantRequest.newBuilder()
    .setRoom("standup").setIdentity("bob").build());
```

Use the overload `create(baseUrl, apiKey, secret, httpClient, timeout)` to supply your own
`java.net.http.HttpClient`, or `TesseraConfig.builder()` for all settings (timeouts, admin token
TTL, short secrets in development). Clients are immutable and thread-safe; create one per
application.

Every call mints a 10-minute admin token carrying only the grant it needs (`room_create`,
`room_list`, `room_admin` scoped to the request's room, or `room_record`).

Errors are thrown as `TesseraException` with the Twirp code and HTTP status:

```java
try {
  rooms.getParticipant(GetParticipantRequest.newBuilder().setRoom("r").setIdentity("x").build());
} catch (TesseraException e) {
  if (TesseraException.NOT_FOUND.equals(e.getCode())) { /* ... */ }
}
```

### Recordings

```java
import io.github.keshavgoyal26.tessera.RecordingServiceClient;

RecordingServiceClient recordings = RecordingServiceClient.create(baseUrl, apiKey, secret);

RecordingInfo rec = recordings.startRecording(StartRecordingRequest.newBuilder()
    .setRoom("standup")
    .setParticipant(ParticipantRecordingTarget.newBuilder().setIdentity("alice"))
    .setHls(HLSOptions.newBuilder().setSegmentDurationSeconds(4))
    .build()).getRecording();

recordings.stopRecording(StopRecordingRequest.newBuilder()
    .setRecordingId(rec.getRecordingId()).build());
```

### Webhooks

```java
import io.github.keshavgoyal26.tessera.WebhookReceiver;
import io.github.keshavgoyal26.tessera.WebhookVerificationException;

WebhookReceiver receiver = new WebhookReceiver("APIxxxxxxxx", System.getenv("TESSERA_API_SECRET"));
// or new WebhookReceiver(Map.of("APIold", oldSecret, "APInew", newSecret)) during key rotation

try {
  WebhookEvent event = receiver.receive(rawBodyBytes, headersAsMap);
  switch (event.getType()) {
    case WEBHOOK_EVENT_TYPE_PARTICIPANT_JOINED -> { /* ... */ }
    default -> { }
  }
} catch (WebhookVerificationException e) {
  // respond 401
}
```

The receiver checks `Tessera-Signature: t=<unix>,v1=<hex HMAC-SHA256(secret, "<t>.<body>")>`
in constant time against the secret selected by `Tessera-Key`, rejects timestamps more than
300 s from now (`withTolerance(...)`), and matches header names case-insensitively. Pass the
**raw** body bytes. `application/protobuf` bodies are parsed as binary; other bodies as protojson,
which needs `protobuf-java-util` on the classpath (otherwise a `TesseraException` explains what
to add). Deliveries are at-least-once: deduplicate on `event.getId()`.
`WebhookReceiver.sign(secret, timestamp, body)` produces a valid header value for tests.

## Dependency policy

- Runtime: **`com.google.protobuf:protobuf-java` only** (4.36.2).
- Optional: `com.google.protobuf:protobuf-java-util` (same version), only for JSON webhooks.
- HTTP uses `java.net.http.HttpClient`, JWT signing uses `javax.crypto.Mac`, and JSON for token
  claims and Twirp errors uses a small internal writer/reader. No Jackson, Gson, OkHttp or
  logging framework.
- Packages under `io.github.keshavgoyal26.tessera.internal` are not public API.

## Code generation

The protobuf model and request/response classes are not checked in. During
`generate-sources`, [`io.github.ascopes:protobuf-maven-plugin`](https://ascopes.github.io/protobuf-maven-plugin/)
downloads `protoc` 4.36.2 from Maven Central and compiles `../../proto/tessera/v1/*.proto` into
`target/generated-sources/protobuf` (package `io.github.keshavgoyal26.tessera.proto.v1`, from each
file's `java_package` option). The generated Java therefore always matches `proto/`. Changing a
`.proto` file only needs a rebuild.

## Building

Requires JDK 17+ and Maven 3.9+ (checked by `maven-enforcer-plugin`).

```sh
mvn -B verify
```
