# Tessera web demo

Two pages built with [vite](https://vite.dev) and the local [`@tessera/client`](../../sdk/js) SDK:

- **`/` Live** (`index.html`, `src/live.ts`): presenters stream to the server, viewers see every
  presenter and watch one or more at a time. Name and room fields, an optional *Record me* tick, and
  *Go live* / *Watch*. Query parameters: `?room=` (default `room1`), `?url=` (server),
  `?name=` (default a random `Guest-NNNN`), `?record=1`.
- **`/call.html` Video call** (`call.html`, `src/main.ts`): everyone sees everyone. Dark responsive
  grid, mute icons, active speaker ring, quality badge, receive-quality selector, stats overlay,
  screen share and a copy-invite-link button.

## Run

Easiest: `scripts/demo.sh` from the repository root (see [docs/DEMO.md](../../docs/DEMO.md)).

By hand:

```sh
go run ./cmd/tessera --dev          # serves GET /dev/token for the demo
(cd sdk/js && npm ci && npm run build)
cd examples/web-demo && npm ci
npm run dev       # http://localhost:5173
npm run build     # type-check and bundle to dist/
```

On the call page, enter a name and room and press Join: the page fetches a token from `/dev/token` on the same
host (port 7880). To use another server, open *Advanced* and enter its URL and a token (tokens
for real deployments come from your backend, see [examples/java-spring](../java-spring)).
Query parameters: `?room=&name=` prefill the card, `&autojoin=1` joins immediately,
`&url=&token=` fill the advanced fields. `window.room` exposes the `Room` for debugging.
