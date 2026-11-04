# Tessera Spring Boot example

A Spring Boot 4 backend showing the whole server-side integration with the
[Java SDK](../../sdk/java): issue join tokens to the browser, create rooms, start and stop
recordings and receive webhooks.

| Endpoint | What it does |
|----------|--------------|
| `POST /api/token?room=&identity=&name=&record=` | Returns `{url, token}` for the browser SDK (`room_join` grant, `room_config.recording_enabled` when `record=true`). A real app derives the identity from its session. |
| `POST /api/rooms/{name}` / `GET /api/rooms` / `DELETE /api/rooms/{name}` | Create, list and delete rooms |
| `GET /api/rooms/{name}/participants`, `DELETE /api/rooms/{name}/participants/{identity}` | List and remove participants |
| `POST /api/rooms/{name}/recordings`, `DELETE /api/recordings/{id}`, `GET /api/recordings` | Start, stop and list recordings |
| `POST /webhooks/tessera` | Verifies the HMAC signature, deduplicates by event id, logs the event |
| `GET /api/events` | Last webhook events received (for trying it out) |

## Run it

```sh
# 1. a Tessera server in development mode (prints a development key and secret)
go run ./cmd/tessera --dev

# 2. install the SDK locally and start the example
(cd sdk/java && mvn -B install -DskipTests)
cd examples/java-spring
TESSERA_API_KEY=<key> TESSERA_API_SECRET=<secret> mvn spring-boot:run

# 3. use it
curl -X POST 'localhost:8080/api/token?room=demo&identity=alice&record=true'
curl -X POST  localhost:8080/api/rooms/demo
curl -X POST  localhost:8080/api/rooms/demo/recordings
curl          localhost:8080/api/events
```

Point the Tessera webhook endpoint at `http://localhost:8080/webhooks/tessera`
(`webhook.endpoints` in `tessera.yaml`, `api_key` selects the signing secret). The browser demo
(`examples/web-demo`) can fetch its token from `POST /api/token` instead of using a hard-coded one.

Configuration: `tessera.url`, `tessera.api-key`, `tessera.api-secret`
(environment `TESSERA_URL`, `TESSERA_API_KEY`, `TESSERA_API_SECRET`).

The webhook endpoint uses the default JSON encoding, so the example adds `protobuf-java-util`.
Configure the endpoint with `encoding: protobuf` to drop that dependency.

`mvn -B test` runs MockMvc tests for token issuing and webhook verification.
