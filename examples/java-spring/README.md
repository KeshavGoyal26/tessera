# Tessera Spring Boot example (placeholder)

This directory is a **placeholder** for a Spring Boot 4.1.2 example backend using the
[Java server SDK](../../sdk/java). It holds a skeleton `pom.xml` and code sketches only. It is
**not built in CI** and not yet expected to compile against a running server; the full example
lands with the SDK implementation in WP3.

## Planned contents

- `POST /api/token`: returns `{url, token}` with a join token minted by `AccessToken`
  (`TokenController`). A real app authenticates the user first and derives the identity from
  its session.
- `POST /webhooks/tessera`: verifies and decodes webhooks with `WebhookReceiver`, answering 401 on
  verification failures (`WebhookController`). The body is bound as raw `byte[]` so the
  signature can be checked.
- A `RoomServiceClient` bean for admin calls (`Application`).
- Configuration through `tessera.url`, `tessera.api-key`, `tessera.api-secret`
  (`application.properties`, environment variables `TESSERA_API_KEY` / `TESSERA_API_SECRET`).

## Trying the skeleton locally

```sh
(cd ../../sdk/java && mvn -B install)   # installs tessera-server-sdk 0.1.0-SNAPSHOT
mvn -B spring-boot:run
```

The example adds `protobuf-java-util` because webhook endpoints default to JSON encoding.
