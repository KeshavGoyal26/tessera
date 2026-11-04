# Tessera web demo

Browser client built with [vite](https://vite.dev) and the local [`@tessera/client`](../../sdk/js)
SDK: join form, responsive video grid, microphone / camera / screen-share toggles, device
pickers, per-view quality selector, speaker highlight, connection quality badge and a network
stats overlay (bitrate, resolution, fps, loss, jitter, RTT).

## Run

```sh
# 1. a server and a token
go run ./cmd/tessera --dev
go run ./cmd/tessera create-token --room demo --identity alice   # run again with --identity bob

# 2. the demo
(cd sdk/js && npm ci && npm run build)
cd examples/web-demo && npm ci
npm run dev       # http://localhost:5173
npm run build     # type-check and bundle to dist/
```

Enter `ws://localhost:7880` and a token, or open
`http://localhost:5173/?url=ws://localhost:7880&token=<jwt>&autojoin=1`. Tokens for real
deployments come from your backend (see [examples/java-spring](../java-spring)).
`window.room` exposes the `Room` for debugging.
