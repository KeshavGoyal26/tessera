# Tessera Server SDK for Java

Java 17+ server SDK for [Tessera](../../README.md): mint access tokens, manage rooms,
participants and recordings, and verify webhooks. One required dependency (`protobuf-java`),
sync and `CompletableFuture` APIs, timeouts, automatic retries and typed exceptions.

## Quick start

```java
var rooms = RoomServiceClient.create("https://tessera.example.com", "APIkey", System.getenv("TESSERA_SECRET"));
rooms.createRoom(CreateRoomRequest.newBuilder().setName("standup").build());            // or createRoomAsync(...)
String jwt = AccessToken.builder().apiKey("APIkey").secret(System.getenv("TESSERA_SECRET"))
    .identity("alice").grants(RoomGrant.builder().room("standup").roomJoin(true).build()).toJwt();
// hand `jwt` to the browser SDK: new Room().connect(url, jwt)
```

Runnable end to end in [`examples/java-spring`](../../examples/java-spring).

## Install

```xml
<dependency>
  <groupId>io.github.keshavgoyal26</groupId>
  <artifactId>tessera-server-sdk</artifactId>
  <version>0.1.0</version>
</dependency>
<!-- Only if your webhook endpoints use the default JSON encoding: -->
<dependency>
  <groupId>com.google.protobuf</groupId>
  <artifactId>protobuf-java-util</artifactId>
  <version>4.36.2</version>
</dependency>
```

Gradle: `implementation("io.github.keshavgoyal26:tessera-server-sdk:0.1.0")`. Until the first
release is on Maven Central, run `mvn -B install` in this directory and use `0.1.0-SNAPSHOT`.

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

### Async, timeouts and retries

Every method has an `...Async` variant returning a `CompletableFuture` that completes
exceptionally with the same `TesseraException`:

```java
rooms.listRoomsAsync(ListRoomsRequest.getDefaultInstance())
     .thenAccept(r -> r.getRoomsList().forEach(x -> System.out.println(x.getName())));
```

`TesseraConfig.builder()` sets `timeout` (default 10 s), `maxRetries` (default 2) and
`retryBackoff` (default 200 ms, doubled per retry). Connection errors, timeouts and
HTTP 429/502/503/504 (`unavailable`) are retried for idempotent calls; `startRecording` is
never retried, and `not_found`, `permission_denied` etc. fail immediately.

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

## Room settings on join

`RoomGrant.builder().roomConfig(RoomGrant.RoomConfig.of(emptyTimeout, departureTimeout, maxParticipants, recordingEnabled))`
applies when the join auto-creates the room. `recordingEnabled` makes browser publishers prefer H.264.

## Contract tests

`src/test/.../ContractTest` checks token claims, webhook bodies and signatures and Twirp
messages against golden fixtures in [`testdata/contract`](../../testdata/contract) generated
by Go (`go test ./pkg/admin -run Golden -update`). `CrossLanguageTest` builds and starts the real
Go admin server (`pkg/admin/cmd/adminfake`) and exercises the SDK against it, including a
Java-minted token accepted by Go and a Go-signed webhook verified by Java (skipped when `go` is
not installed).

## Releasing

`mvn -B verify` produces the jar plus `-sources` and `-javadoc` jars. A release is
`mvn -B -P release deploy` (GPG signing and the Central Portal publishing plugin, credentials
via `settings.xml` server `central` and `MAVEN_GPG_PASSPHRASE`); the profile is not active by default.

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
