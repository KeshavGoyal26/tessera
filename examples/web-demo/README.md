# Tessera web demo

Minimal browser client built with [vite](https://vite.dev) and the local
[`@tessera/client`](../../sdk/js) SDK: join form, video grid, microphone and camera toggles.

The SDK is still scaffolding, so joining currently shows a `NotImplementedError` in the page.

## Run

The demo depends on the SDK through `file:../../sdk/js`, so build the SDK first:

```sh
(cd ../../sdk/js && npm ci && npm run build)
npm ci
npm run dev       # http://localhost:5173
npm run build     # type-check and bundle to dist/
```

Enter the server URL (for example `ws://localhost:7880`) and an access token for a room.
