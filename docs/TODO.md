# Known issues (for later)

From the functional QA of 2026-11-09. Severity in brackets.

## Server / cluster
- [medium] Subscriber transceivers are never reused: a viewer's SDP grows with presenter churn
  (`pkg/rtc/transport.go`). Risky to change.
- [low] Edges never report active speakers to their viewers.
- [low] A clean SIGTERM raises the "node gone" alert; media nodes have no drain before exit.
- [low] `/rtc/route` (ADR-0012) is not implemented: clients pick an edge themselves.
- [low] Edge `bps_in` undercount (seen once).
- [low] Relay-only rooms can keep a room alive on a media node (C7; mostly moot with random placement).
- [low] Local cluster (`scripts/cluster-start.sh`) runs without TURN, so the presenter → edge TURN
  path is not exercised locally.

## Recording
- [low] After a crash (kill -9) `quality.json` is never finalised: storage has no `Get`.

## Dashboard / demo
- [low] Theme toggle remounts the view: clears the filter and closes the player (`web/admin/src/main.ts` toggleTheme).
- [low] Native-HLS player shows no error on failure.
- [low] Cluster landing page node picker (QA a5 B11).
- [low] Call page: tile order flips after a full rejoin.
- [low] Live demo rewind timeline: grey/fade near the live edge, no buffering cue.
- [low] Optional: show presenters proxied through an edge on that edge's dashboard pages.


## From the functional bug hunt (2026-11-10)
- [low] For up to 10 s after a media node crashes (until its heartbeat key expires) a rejoining
  presenter can still be placed on it, costing one SDK reconnect attempt.
- [low] Live demo: browser Forward after Back shows the room URL with the join card, disconnected.
- [low] SDK `#reconnect` maps refused resumes (not_allowed/not_found) to SIGNAL_TIMEOUT: the user
  sees "connection lost" instead of the reason (code reading, not reproduced).
- [low] Dashboard: a participant recording waiting for the participant to publish shows STARTING
  with no hint ("waiting for tracks").
- Not covered yet: browser through cluster edges, recording playback via an edge, kick/metadata
  across edges, Redis restart, edge kill, real token expiry, camera-denied path.
