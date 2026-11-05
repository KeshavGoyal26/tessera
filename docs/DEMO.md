# Live demo guide

A five-minute presenter script for Tessera, plus troubleshooting. Everything runs natively
(Go 1.22+, Node 20+), no Docker.

## Start

```bash
scripts/demo.sh              # server, recorder, web demo
scripts/demo.sh --loadtest   # same, plus background traffic from tessera-loadtest
```

It builds the binaries into `.demo/bin`, writes configs under `.demo/`, and prints:

| What | URL |
|------|-----|
| Live demo (present / watch) | `http://localhost:5173/` |
| Video call demo | `http://localhost:5173/call.html` |
| Dashboard | `http://localhost:7880/admin/`, sign in with `devkey` / `devsecret-devsecret-devsecret-0000` |
| Live stats | `http://localhost:6789/stats` |
| Dev token API | `http://localhost:7880/dev/token?room=live&identity=me&role=presenter` (only with `--dev`) |

Ctrl-C stops everything. Logs are in `.demo/logs/`, recordings in `.demo/recordings/`.
`scripts/demo.sh --host <ip>` overrides the address printed and advertised for media.

Before the audience arrives: open `http://localhost:5173/` in **three browser windows** (or add a
phone, see LAN below), allow camera and microphone once, and leave the dashboard open in another
tab, signed in.

## The live demo

Presenters stream to the server; viewers see every presenter and watch one or more at a time. It is
not a call: presenters receive nothing and never see viewers.

The page has one field (your name), a **Record me when I present** tick, and two buttons:

- **Go live** streams camera and mic. The bar has Mic, Camera, Screen and End.
- **Watch** opens the viewer: a live list of presenters (with camera, mic, screen and REC state) and
  a stage. The first presenter is shown straight away; click others in the list to add them, or
  **Watch all**. Each tile has its own mute and ✕. Presenters you are not watching cost no
  bandwidth, and each tile gets the simulcast layer that fits its size.

Everyone lands in the channel `live`; open `?room=<name>` for another one. `?url=ws://host:7880`
points the page at another server.

## The 5-minute script

**0:00 Go live (45 s).** In window A type a name, tick **Record me**, press **Go live**. In window B,
another name, **Go live** (no tick). No token pasting: the page fetched one from `/dev/token`.

**0:45 Watch (60 s).** In window C type a name and press **Watch**. A's stream appears at once and
both presenters are in the list, A with a **REC** marker. Click B to add it beside A, ✕ to drop it,
**Watch all** to bring it back. Mute one tile. Press **Screen** in B: C gets a separate
"(screen)" tile. Say: each presenter uploads once, the SFU sends each viewer only what they watch.

**1:45 Quality (30 s).** Tiles start at 720p within a few seconds: the server probes for spare
bandwidth before stepping a viewer up a simulcast layer, so there is no blurry warm-up.

**2:15 Dashboard (90 s).** Switch to the dashboard tab.
1. *Overview*: CPU, bandwidth, packets, and the room and participant counts.
2. *Rooms*: expand `live` → presenter → tracks.
3. *Track quality*: select A's video. Per-layer bitrate, fps, resolution, loss, and each
   subscriber's current and target layer.
4. *Connectivity*: ICE candidate types, RTT.

**3:45 Recording (45 s).** Press **End** in A. In the dashboard open *Recordings*: A's recording is
listed with room, start time, duration and status. Click **▶ Play** to watch it with video and audio.
You can also record anyone from *Rooms* with **Record** (and **■ Stop** in *Recordings*). The files
are HLS (fMP4) under `.demo/recordings/<room>/<recording_id>/`, served by the recorder at
`http://127.0.0.1:6790/recordings/` because `storage.local_base_url` is set in the demo's recorder
config. To get an MP4: `ffmpeg -i .demo/recordings/<room>/<recording_id>/master.m3u8 -c copy out.mp4`.

**4:30 Load (30 s).** Start with `--loadtest` (or in another terminal
`.demo/bin/tessera-loadtest --secret devsecret-devsecret-devsecret-0000 --rooms 5 --publishers 6`).
Show the Overview charts and `/stats` filling up while the live channel stays smooth.

The classic video call (everyone sees everyone, active-speaker ring, quality selector, stats
overlay) is still at `/call.html`.

## Tokens for your own backend

The dev endpoint takes `role=presenter|viewer` and `record=1`. In production, issue the same grants
from your backend, for example with the Java SDK:

```java
// presenter: publish only; autoRecord(true) records them from join until they leave
RoomGrant.builder().room("live").roomJoin(true).canSubscribe(false).build();
// viewer: subscribe only, invisible to others; connect with autoSubscribe: false and
// subscribe per presenter (RemoteTrackPublication.setSubscribed(true))
RoomGrant.builder().room("live").roomJoin(true).canPublish(false).canPublishData(false).hidden(true).build();
```

## Troubleshooting

- **Camera or microphone is blocked / `getUserMedia` undefined.** Browsers allow capture only on
  `localhost` or HTTPS. Use `http://localhost:5173` on the demo machine. Check the lock icon in the
  address bar and the OS privacy settings (macOS: System Settings → Privacy → Camera).
- **Port already in use.** The script checks 7880 and 5173 first. Find the owner with
  `lsof -i :7880`. The recorder uses 6790 and the server metrics and `/stats` use 6789.
- **LAN demo (second device).** Run `scripts/demo.sh --host 192.168.1.20` (your machine's IP) and
  open `http://192.168.1.20:5173/` on the phone. The web page itself is plain HTTP, so the phone
  browser will refuse camera access. Options: in Chrome open `chrome://flags/#unsafely-treat-insecure-origin-as-secure`,
  add `http://192.168.1.20:5173` and relaunch (desktop and Android), or front the demo with an HTTPS
  tunnel. Viewing without a camera works as is. Allow inbound TCP 7880 (signaling), UDP 7882 and TCP 7881 (media)
  in the host firewall.
- **Joined but no video from the other side.** Media goes over UDP; confirm the firewall allows it
  and that the page host matches the IP the server advertises (`--host`). VPNs and Docker bridges
  can confuse the choice of address.
- **"This server has no /dev/token".** The server is not running with `--dev`. Paste a server URL
  and token under *Advanced* instead (`tessera create-token --room demo --identity me`).
- **Dashboard sign-in fails.** Use the key and secret printed by the script, not a token.
- **Reset everything.** Ctrl-C, then `rm -rf .demo`.
