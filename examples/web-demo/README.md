# Tessera web demo

Browser client built with [vite](https://vite.dev) and the local [`@tessera/client`](../../sdk/js)
SDK: landing card (name and room), dark responsive video grid, name labels, mic and camera
muted icons, active speaker ring, connection quality badge, control bar (mic, camera, screen
share, receive-quality selector, stats overlay, leave) and a copy-invite-link button.

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

Enter a name and room and press Join: the page fetches a token from `/dev/token` on the same
host (port 7880). To use another server, open *Advanced* and enter its URL and a token (tokens
for real deployments come from your backend, see [examples/java-spring](../java-spring)).
Query parameters: `?room=&name=` prefill the card, `&autojoin=1` joins immediately,
`&url=&token=` fill the advanced fields. `window.room` exposes the `Room` for debugging.
