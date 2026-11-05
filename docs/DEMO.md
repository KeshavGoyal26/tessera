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
| Web demo | `http://localhost:5173/` |
| Dashboard | `http://localhost:7880/admin/`, sign in with `devkey` / `devsecret-devsecret-devsecret-0000` |
| Live stats | `http://localhost:6789/stats` |
| Dev token API | `http://localhost:7880/dev/token?room=demo&identity=me` (only with `--dev`) |

Ctrl-C stops everything. Logs are in `.demo/logs/`, recordings in `.demo/recordings/`.
`scripts/demo.sh --host <ip>` overrides the address printed and advertised for media.

Before the audience arrives: open the web demo in **two browser windows** (or a laptop and a
phone, see LAN below), allow camera and microphone once, and leave the dashboard open in a third
tab, signed in.

## The 5-minute script

**0:00 Join (45 s).** In window A enter a name and the room `demo`, press **Join**. No token
pasting: the page fetched one from `/dev/token`. Click **Copy invite link**, open it in window B,
type another name, **Join**. Turn on mic and camera in both. Point out the tiles, name labels,
mute icons and the connection quality bars.

**0:45 Active speaker (30 s).** Talk in A: its tile gets the glowing green ring in both windows.
Mute the mic and show the red muted icon on the other side.

**1:15 Simulcast quality (60 s).** In B open **Stats**: note receive resolution and bitrate for
A's video. Change **Quality** to Low, then High. Resolution and bitrate move (for example 320x180
at about 150 kbps against 1280x720 at about 1.3 Mbps) with no renegotiation or reconnect.
Switch back to Auto. Say: one upload from A, the SFU picks which layer each viewer receives.

**2:15 Screen share (30 s).** Press **Share** in A, pick a window. B gets a separate "(screen)"
tile. Stop sharing.

**2:45 Dashboard (75 s).** Switch to the dashboard tab.
1. *Overview*: CPU, bandwidth, packets, and the room and participant counts.
2. *Rooms*: expand `demo` → participant → tracks.
3. *Track quality*: select A's video. Per-layer bitrate, fps, resolution, loss, and the
   subscriber's current and target layer. Flip B's Quality selector and watch the layer switch.
4. *Connectivity*: ICE candidate types, RTT.

**4:00 Recording (45 s).** In *Rooms*, click **Record** on a participant. Open *Recordings*: the job
shows under *Jobs* as starting, then active, with a **Stop** button. Wait about 10 s and press **Stop**.
The finished recording appears under *Finished* with a **▶** button per track that plays it in the
dashboard. The files are HLS (fMP4) under `.demo/recordings/<room>/<recording_id>/`, served by the
recorder at `http://127.0.0.1:6790/recordings/` because `storage.local_base_url` is set in the demo's
recorder config. To get an MP4: `ffmpeg -i <track>/index.m3u8 -c copy out.mp4`.

**4:45 Load (15 s).** Start with `--loadtest` (or in another terminal
`.demo/bin/tessera-loadtest --secret devsecret-devsecret-devsecret-0000 --rooms 5 --publishers 6`).
Show the Overview charts and `/stats` filling up while the two live calls stay smooth.

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
