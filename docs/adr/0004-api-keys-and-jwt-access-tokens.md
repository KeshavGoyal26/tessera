# ADR-0004: API keys and HS256 JWT access tokens

- Status: accepted
- Date: 2026-11-04
- Deciders: Tessera maintainers

## Context

Application backends must be able to authorize end users to join a room with specific
permissions, without the media server knowing anything about the application's users.
Backends must also call the admin API. LiveKit's model (API key/secret pairs, short-lived JWTs
with grants) is well understood by developers and easy to implement in every language.

## Decision

1. The server is configured with one or more **API key / secret** pairs (`keys:` in the config
   or a `key_file`, never logged). Secrets must be at least 32 bytes; the server refuses to
   start with shorter secrets unless `development: true`.
2. **Access tokens are JWTs signed with HS256** using the secret of the key in `iss`.
   Other algorithms (including `none`) are rejected.
3. Claims (JSON, all names are part of the public contract and covered by contract tests):

   | Claim | Type | Meaning |
   |-------|------|---------|
   | `iss` | string | API key |
   | `sub` | string | participant **identity** (unique per room); required for `room_join` |
   | `nbf`, `exp` | number | validity; max lifetime 24 h for join tokens; 60 s leeway |
   | `jti` | string | optional unique id |
   | `name` | string | display name |
   | `metadata` | string | opaque application metadata |
   | `attributes` | object<string,string> | optional key/value attributes |
   | `grants` | object | see below |

   `grants` object:

   | Field | Type | Meaning |
   |-------|------|---------|
   | `room` | string | room the grant applies to (required for `room_join`, `room_admin`) |
   | `room_join` | bool | may join `room` |
   | `room_create` | bool | admin API: create/delete rooms |
   | `room_list` | bool | admin API: list rooms |
   | `room_admin` | bool | admin API: manage participants of `room` (or all rooms if `room` empty) |
   | `room_record` | bool | admin API: start/stop/list recordings |
   | `recorder_worker` | bool | recorder worker API (claim jobs, report status) |
   | `can_publish` | bool (default true) | may publish media |
   | `can_publish_sources` | string[] | restrict sources: `camera`, `microphone`, `screen_share`, `screen_share_audio` |
   | `can_subscribe` | bool (default true) | may subscribe |
   | `can_publish_data` | bool (default true) | may send data packets |
   | `can_update_own_metadata` | bool | may update own name/metadata/attributes |
   | `hidden` | bool | invisible to other participants (recorders, monitors) |
   | `recorder` | bool | marks a recorder participant |
   | `room_config` | object | optional room settings applied when the join auto-creates the room |

4. Admin API calls use `Authorization: Bearer <jwt>` with the relevant admin grant. SDKs mint a
   short-lived (default 10 min) token per call.
5. **Token refresh**: the server sends a fresh token (`SignalResponse.refresh_token`) to
   connected clients before expiry so that reconnects work in long sessions.
6. **Webhooks** are authenticated separately with HMAC-SHA256 (ADR-0008 and PROTOCOL.md).
7. Libraries: Go uses `github.com/golang-jwt/jwt/v5`; Java uses `javax.crypto.Mac` with a
   tiny built-in JSON writer (no runtime dependency); the browser SDK never mints tokens.

## Consequences

- Stateless authorization; tokens can be minted by any backend with the secret.
- Revocation before `exp` is not possible without extra state; mitigated by short lifetimes,
  `RemoveParticipant` and `UpdateParticipant` (permission downgrade) in the admin API.
- HS256 means every verifier knows the secret. Acceptable because only Tessera nodes verify.
  Asymmetric keys (EdDSA) can be added later with a `kid` header without breaking HS256.

## Alternatives considered

- **Opaque tokens + introspection**: requires a callback to the application per join.
- **RS256/EdDSA now**: more key management for every integrator; deferred.
