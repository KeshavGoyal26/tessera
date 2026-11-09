// Presenter / viewer demo. Presenters publish to the server and receive nothing.
// Viewers are hidden, see every presenter and subscribe only to the ones they watch.
//
// Query parameters: ?room= (default "room1"), ?name= (default a random Guest-NNNN),
// ?url= (server, default ws://<host>:7880), ?record=1. window.room exposes the Room for debugging.
//
// A recorded presenter's live recording is announced in their `hls_url` attribute: a viewer can
// switch that tile to the HLS player (Rewind) to scrub back, then return to WebRTC (Live).
import { ConnectionState, Room, RoomEvent, TrackSource } from "@tessera/client";
import type { RemoteParticipant, RemoteTrack, RemoteTrackPublication } from "@tessera/client";
import { attachTimeline, liveLevel, setDot, timelineUrl } from "./quality";
import { leftMessage } from "./reasons";

type Role = "presenter" | "viewer";

const $ = <T extends HTMLElement = HTMLElement>(id: string) => document.getElementById(id) as T | null;
const el = <T extends HTMLElement = HTMLElement>(id: string) => $<T>(id)!;
const form = el<HTMLFormElement>("join");
const field = (name: string) => form.elements.namedItem(name) as HTMLInputElement;
const errText = (err: unknown) => (err instanceof Error ? err.message : String(err));

const params = new URLSearchParams(location.search);
let channel = "";
const serverUrl = params.get("url") || `ws://${location.hostname}:7880`;

let room: Room | undefined;
let toastTimer: number | undefined;

function toast(msg: string): void {
  const t = el("toast");
  t.textContent = msg;
  t.hidden = false;
  window.clearTimeout(toastTimer);
  toastTimer = window.setTimeout(() => (t.hidden = true), 2500);
}

// ---- landing ---------------------------------------------------------------

field("name").value = (params.get("name") ?? `Guest-${Math.floor(1000 + Math.random() * 9000)}`).slice(0, 40);
field("room").value = (params.get("room") || "room1").slice(0, 64);
field("record").checked = params.get("record") === "1";
try {
  const why = sessionStorage.getItem("tessera-left");
  sessionStorage.removeItem("tessera-left");
  if (why) {
    el("error").textContent = why;
    el("error").hidden = false;
  }
} catch { /* storage unavailable */ }
// A reload while in a room lands here on the room's history entry; make it the join card's.
history.replaceState(null, "", location.href);

form.addEventListener("submit", (e) => {
  e.preventDefault();
  const role = ((e.submitter as HTMLButtonElement | null)?.value as Role) || "presenter";
  void join(role);
});

async function fetchToken(name: string, role: Role, record: boolean): Promise<string> {
  const http = serverUrl.replace(/^ws/, "http").replace(/\/$/, "");
  const slug = name.toLowerCase().replace(/[^a-z0-9]+/g, "-") || "guest";
  const q = new URLSearchParams({ room: channel, identity: `${role[0]}-${slug}-${Math.random().toString(36).slice(2, 6)}`, name, role });
  if (record) q.set("record", "1");
  const res = await fetch(`${http}/dev/token?${q}`).catch(() => {
    throw new Error(`Cannot reach ${http}. Is the server running with --dev?`);
  });
  if (!res.ok) throw new Error(`Token request failed: ${res.status} ${await res.text()}`);
  return ((await res.json()) as { token: string }).token;
}

async function join(role: Role): Promise<void> {
  const name = field("name").value.trim();
  channel = field("room").value.trim();
  if (!name || !channel) {
    el("error").textContent = "Enter a name and a room.";
    el("error").hidden = false;
    return;
  }
  // Keep the room in the URL so a reload (after leaving) comes back to it.
  params.set("room", channel);
  history.replaceState(null, "", `${location.pathname}?${params}${location.hash}`);
  el("error").hidden = true;
  const buttons = form.querySelectorAll("button");
  for (const b of buttons) b.disabled = true;
  // Nobody auto-subscribes: presenters cannot subscribe, viewers choose.
  // H.264: recordings are never transcoded, so a presenter recorded later (dashboard "Record") must send it.
  const r = new Room({ autoSubscribe: false });
  try {
    await r.connect(serverUrl, await fetchToken(name, role, field("record").checked));
    room = r;
    (window as unknown as { room: Room }).room = r;
    r.on(RoomEvent.Reconnecting, () => {
      rejoining = true;
      setStatus(ConnectionState.Reconnecting);
    })
      .on(RoomEvent.Reconnected, () => {
        rejoining = false;
        setStatus(ConnectionState.Connected);
        render(); // presenters gone for good are dropped now
      })
      .on(RoomEvent.Disconnected, (reason) => {
        // Say why on the join card the reload brings back (kicked, room ended, ...).
        try {
          const why = leftMessage(reason);
          if (why) sessionStorage.setItem("tessera-left", why);
        } catch { /* storage unavailable */ }
        location.reload();
      });
    // In the room is its own history entry: Back (or Leave) returns to the join card.
    history.pushState({ inRoom: true }, "", location.href);
    for (const n of document.querySelectorAll("[data-room]")) n.textContent = channel;
    setStatus(ConnectionState.Connected);
    el("landing").hidden = true;
    if (role === "presenter") await present(r, name);
    else watchRoom(r);
  } catch (err) {
    void r.disconnect().catch(() => undefined);
    el("error").textContent = errText(err);
    el("error").hidden = false;
  } finally {
    for (const b of buttons) b.disabled = false;
  }
}

function setStatus(state: ConnectionState): void {
  for (const n of document.querySelectorAll<HTMLElement>("[data-status]")) {
    n.textContent = state;
    n.dataset.state = state;
  }
}

// ---- presenter -------------------------------------------------------------

async function present(r: Room, name: string): Promise<void> {
  el("present").hidden = false;
  watchScreenEnd(r);
  el("preview-name").textContent = `${name} (you)`;
  el("preview-avatar").textContent = name.charAt(0).toUpperCase();
  el("preview").classList.toggle("recorded", r.localParticipant.attributes?.recorded === "true");
  // Go live with camera and mic; if one is refused, present with what works.
  await setCam(true).catch((err) => toast(`Camera: ${errText(err)}`));
  await setMic(true).catch((err) => toast(`Microphone: ${errText(err)}`));
}

const pressed = (id: string) => el(id).getAttribute("aria-pressed") === "true";

async function setMic(on: boolean): Promise<void> {
  await room?.localParticipant.setMicrophoneEnabled(on);
  el("p-mic").setAttribute("aria-pressed", String(on));
}

async function setCam(on: boolean): Promise<void> {
  const pub = await room?.localParticipant.setCameraEnabled(on);
  el("p-cam").setAttribute("aria-pressed", String(on));
  const preview = el("preview");
  preview.querySelector("video")?.remove();
  if (on && pub) {
    const v = pub.track.attach() as HTMLVideoElement;
    v.muted = true;
    preview.prepend(v);
  }
  preview.classList.toggle("has-video", on && !!pub);
}

async function setScreen(on: boolean): Promise<void> {
  const pub = await room?.localParticipant.setScreenShareEnabled(on);
  el("p-screen").setAttribute("aria-pressed", String(on));
  $("preview-screen")?.remove();
  const stage = el("present-stage");
  if (on && pub) {
    const tile = document.createElement("div");
    tile.className = "tile screen has-video";
    tile.id = "preview-screen";
    tile.append(pub.track.attach());
    tile.insertAdjacentHTML("beforeend", '<div class="label"><span class="nm">Your screen</span></div>');
    stage.append(tile);
  }
  stage.dataset.count = String(stage.querySelectorAll(".tile").length);
}

// The browser's own "Stop sharing" ends the share without our button.
function watchScreenEnd(r: Room): void {
  r.on(RoomEvent.LocalTrackUnpublished, (pub) => {
    if (pub.source !== TrackSource.ScreenShare) return;
    el("p-screen").setAttribute("aria-pressed", "false");
    $("preview-screen")?.remove();
    const stage = el("present-stage");
    stage.dataset.count = String(stage.querySelectorAll(".tile").length);
  });
}

const onClick = (id: string, fn: () => Promise<unknown>) =>
  el(id).addEventListener("click", () => void fn().catch((err) => toast(errText(err))));
onClick("p-mic", () => setMic(!pressed("p-mic")));
onClick("p-cam", () => setCam(!pressed("p-cam")));
onClick("p-screen", () => setScreen(!pressed("p-screen")));
onClick("p-leave", async () => leave());

// ---- viewer ----------------------------------------------------------------

/** Identities being watched. Subscriptions follow this set. */
const watched = new Set<string>();
/** While the SDK reconnects, presenters may vanish and come back (a rejoin replays them):
 *  that is not leaving, so watched tiles and replays are kept. */
let rejoining = false;
/** Once the viewer picks or drops someone, stop choosing for them. */
let viewerChose = false;
/** Presenters shown from their HLS recording instead of WebRTC, by identity. */
const ico = (d: string) => `<svg class="ico" viewBox="0 0 24 24" aria-hidden="true"><path d="${d}"/></svg>`;
const ICON = {
  sound: ico("M4 9h4l5-4v14l-5-4H4V9Zm12.5 3a4 4 0 0 0-2-3.5v7a4 4 0 0 0 2-3.5Z"),
  muted: ico("M4 9h4l5-4v14l-5-4H4V9Zm11.3.3 1.4-1.4L19 10.2l2.3-2.3 1.4 1.4-2.3 2.3 2.3 2.3-1.4 1.4-2.3-2.3-2.3 2.3-1.4-1.4 2.3-2.3-2.3-2.3Z"),
  rewind: ico("M11 6v12l-8-6 8-6Zm10 0v12l-8-6 8-6Z"),
  live: ico("M12 7a5 5 0 1 1 0 10 5 5 0 0 1 0-10Z"),
};
const replays = new Map<string, { destroy(): void }>();

const isPresenter = (p: RemoteParticipant) => p.attributes?.role === "presenter" || p.trackPublications.size > 0;
const presenters = () =>
  [...(room?.remoteParticipants.values() ?? [])].filter(isPresenter).sort((a, b) => (a.name || a.identity).localeCompare(b.name || b.identity));
const sending = (p: RemoteParticipant, s: TrackSource) => {
  const pub = p.getTrackPublication(s);
  return !!pub && !pub.isMuted;
};
const tileId = (identity: string, screen = false) => `w-${identity}${screen ? "-screen" : ""}`;

function watchRoom(r: Room): void {
  el("watch").hidden = false;
  r.on(RoomEvent.ParticipantConnected, render)
    .on(RoomEvent.ParticipantDisconnected, (p) => {
      if (!rejoining) gone(p.identity);
    })
    .on(RoomEvent.TrackPublished, (pub, p) => {
      if (watched.has(p.identity) && live(pub, p.identity)) pub.setSubscribed(true);
      render();
    })
    .on(RoomEvent.TrackUnpublished, render)
    .on(RoomEvent.TrackMuted, render)
    .on(RoomEvent.TrackUnmuted, render)
    .on(RoomEvent.ParticipantAttributesChanged, render)
    .on(RoomEvent.ConnectionQualityChanged, render)
    .on(RoomEvent.TrackSubscribed, onSubscribed)
    .on(RoomEvent.TrackUnsubscribed, (track) => {
      for (const m of track.detach()) m.remove();
    });
  render();
}

/** Camera and mic follow the tile's mode; screen share always stays live (it is not recorded). */
const live = (pub: RemoteTrackPublication, identity: string) => pub.source === TrackSource.ScreenShare || !replays.has(identity);

function watch(p: RemoteParticipant): void {
  watched.add(p.identity);
  for (const pub of p.trackPublications.values()) pub.setSubscribed(live(pub, p.identity));
  render();
}

/** A watched presenter left: a tile playing their recording stays (it is finished now). */
function gone(identity: string): void {
  const tile = $(tileId(identity));
  if (!replays.has(identity) || !tile) return unwatch(identity);
  watched.delete(identity);
  if (tile.classList.contains("ended")) return render();
  tile.classList.add("ended");
  tile.querySelector<HTMLButtonElement>('[data-act="replay"]')!.hidden = true;
  tile.querySelector(".nm")!.textContent += " (ended)";
  $(tileId(identity, true))?.remove();
  render();
}

function unwatch(identity: string): void {
  watched.delete(identity);
  stopReplay(identity);
  const p = [...(room?.remoteParticipants.values() ?? [])].find((x) => x.identity === identity);
  if (p) for (const pub of p.trackPublications.values()) pub.setSubscribed(false);
  $(tileId(identity))?.remove();
  $(tileId(identity, true))?.remove();
  render();
}

el("watch-all").addEventListener("click", () => {
  viewerChose = true;
  for (const p of presenters()) if (!watched.has(p.identity)) watch(p);
});
el("w-leave").addEventListener("click", leave);

const MEDIA: [TrackSource, string][] = [
  [TrackSource.Camera, '<svg class="ico" viewBox="0 0 24 24"><path d="M15 8v8H5V8h10Zm2 3 4-3v8l-4-3v-2Z"/></svg>'],
  [TrackSource.Microphone, '<svg class="ico" viewBox="0 0 24 24"><path d="M12 14a3 3 0 0 0 3-3V6a3 3 0 0 0-6 0v5a3 3 0 0 0 3 3Zm5-3a5 5 0 0 1-10 0H5a7 7 0 0 0 6 6.9V21h2v-3.1A7 7 0 0 0 19 11h-2Z"/></svg>'],
  [TrackSource.ScreenShare, '<svg class="ico" viewBox="0 0 24 24"><path d="M3 5h18v11H3V5Zm2 2v7h14V7H5Zm3 11h8v2H8v-2Z"/></svg>'],
];

/** Redraws the presenter list and the stage from room state. */
function render(): void {
  const ps = presenters();
  if (!rejoining) for (const id of watched) if (!ps.some((p) => p.identity === id)) gone(id);
  // Until the viewer chooses, show the first presenter so the stage is never empty.
  const first = ps[0];
  if (!viewerChose && watched.size === 0 && first) return watch(first);

  el("live-count").textContent = `${ps.length} live`;
  el("no-presenters").hidden = ps.length > 0;
  el("presenter-list").replaceChildren(
    ...ps.map((p) => {
      const on = watched.has(p.identity);
      const name = p.name || p.identity;
      const li = document.createElement("li");
      li.className = on ? "watching" : "";
      const media = MEDIA.filter(([s]) => sending(p, s))
        .map(([, icon]) => icon)
        .join("");
      li.innerHTML = `<button type="button" class="presenter" aria-pressed="${on}"><span class="av"></span><span class="who"><span class="pn-row"><i class="cq"></i><span class="pn"></span></span><span class="media">${media}</span></span><span class="go">${on ? "Watching" : "Watch"}</span></button>`;
      setDot(li.querySelector(".cq"), liveLevel(p.connectionQuality));
      li.querySelector(".av")!.textContent = name.charAt(0).toUpperCase();
      li.querySelector(".pn")!.textContent = name;
      if (p.attributes?.recorded === "true") li.querySelector(".pn")!.insertAdjacentHTML("afterend", '<span class="rec-dot">REC</span>');
      li.querySelector("button")!.addEventListener("click", () => {
        viewerChose = true;
        if (on) unwatch(p.identity);
        else watch(p);
      });
      return li;
    }),
  );

  for (const p of ps) if (watched.has(p.identity)) syncTile(p);
  const stage = el("watch-stage");
  const n = stage.querySelectorAll(".tile").length;
  stage.dataset.count = String(Math.min(n, 5));
  el("stage-empty").hidden = n > 0;
  el("stage-empty").textContent = ps.length ? "Pick a presenter to watch." : "Waiting for someone to go live.";
}

/** Creates or updates the presenter's tile; drops the screen tile once sharing stops. */
function syncTile(p: RemoteParticipant): HTMLElement {
  const existing = $(tileId(p.identity));
  const tile = existing ?? document.createElement("div");
  if (!existing) {
    const name = p.name || p.identity;
    tile.id = tileId(p.identity);
    tile.className = "tile";
    tile.innerHTML = `<div class="avatar"></div><div class="label"><i class="cq"></i><span class="nm"></span></div>
      <div class="tile-actions"><button class="icon-btn" data-act="replay" type="button" hidden></button><button class="icon-btn" data-act="audio" type="button" title="Mute">${ICON.sound}</button><button class="icon-btn" data-act="close" type="button" title="Stop watching">✕</button></div>`;
    tile.querySelector(".avatar")!.textContent = name.charAt(0).toUpperCase();
    tile.querySelector(".nm")!.textContent = name;
    tile.querySelector('[data-act="close"]')!.addEventListener("click", () => {
      viewerChose = true;
      unwatch(p.identity);
    });
    tile.querySelector('[data-act="audio"]')!.addEventListener("click", (e) => {
      const muted = tile.classList.toggle("muted-audio");
      const b = e.currentTarget as HTMLButtonElement;
      b.innerHTML = muted ? ICON.muted : ICON.sound;
      b.title = muted ? "Unmute" : "Mute";
      for (const a of tile.querySelectorAll<HTMLMediaElement>("audio, video.replay")) a.muted = muted;
    });
    tile.querySelector('[data-act="replay"]')!.addEventListener("click", () => {
      if (replays.has(p.identity)) goLive(p);
      else void startReplay(p);
    });
    el("watch-stage").append(tile);
  }
  const replay = replays.has(p.identity);
  // Live: the presenter's connection now. Rewinding: the recording's at the playhead (set by its timeline).
  if (!replay) setDot(tile.querySelector(".cq"), liveLevel(p.connectionQuality));
  const btn = tile.querySelector<HTMLButtonElement>('[data-act="replay"]')!;
  btn.hidden = !replay && !p.attributes?.hls_url;
  btn.innerHTML = replay ? ICON.live : ICON.rewind;
  btn.classList.toggle("is-live", replay);
  btn.title = replay ? "Back to the live stream" : "Watch the recording: pause and scrub back";
  tile.classList.toggle("replay", replay);
  tile.classList.toggle("has-video", replay || (!!tile.querySelector("video") && sending(p, TrackSource.Camera)));
  if (!sending(p, TrackSource.ScreenShare)) $(tileId(p.identity, true))?.remove();
  return tile;
}

/** Switches the tile to the presenter's live HLS recording; WebRTC camera and mic are dropped meanwhile. */
async function startReplay(p: RemoteParticipant): Promise<void> {
  const attr = p.attributes?.hls_url;
  const tile = $(tileId(p.identity));
  if (!attr || !tile || replays.has(p.identity)) return;
  const url = new URL(attr, serverUrl.replace(/^ws/, "http")).href; // relative = on the server (--dev)
  const video = document.createElement("video");
  video.className = "replay";
  video.controls = video.playsInline = true;
  video.muted = tile.classList.contains("muted-audio");
  // The recording's connection timeline, drawn over the top of the tile (click to seek).
  const bar = document.createElement("div");
  bar.className = "qbar";
  bar.setAttribute("role", "img");
  const stopTimeline = attachTimeline(video, bar, timelineUrl(url), (l) => setDot(tile.querySelector(".cq"), l, "Connection at this moment"));
  // Registered before hls.js loads, so a second click (Live) during the load cancels this one.
  let player: { destroy(): void } | undefined;
  const entry = { destroy: () => (player?.destroy(), stopTimeline(), bar.remove(), video.remove()) };
  replays.set(p.identity, entry);
  for (const pub of p.trackPublications.values()) if (!live(pub, p.identity)) pub.setSubscribed(false);
  tile.prepend(video);
  tile.append(bar);
  render();
  const { default: Hls } = await import("hls.js");
  if (replays.get(p.identity) !== entry) return; // back to live meanwhile
  if (Hls.isSupported()) {
    const hls = new Hls();
    hls.on(Hls.Events.ERROR, (_e, d) => {
      if (d.fatal) toast(`Recording playback failed: ${d.details}`);
    });
    hls.loadSource(url);
    hls.attachMedia(video);
    player = hls;
  } else {
    video.src = url; // native HLS (Safari without MSE)
    player = { destroy: () => video.removeAttribute("src") };
  }
  void video.play().catch(() => undefined);
}

function goLive(p: RemoteParticipant): void {
  stopReplay(p.identity);
  if (watched.has(p.identity)) for (const pub of p.trackPublications.values()) pub.setSubscribed(true);
  render();
}

function stopReplay(identity: string): void {
  replays.get(identity)?.destroy();
  replays.delete(identity);
}

function onSubscribed(track: RemoteTrack, pub: RemoteTrackPublication, p: RemoteParticipant): void {
  if (!watched.has(p.identity) || !live(pub, p.identity)) {
    pub.setSubscribed(false);
    return;
  }
  const tile = syncTile(p);
  const media = track.attach();
  if (pub.source === TrackSource.ScreenShare) {
    let st = $(tileId(p.identity, true));
    if (!st) {
      st = document.createElement("div");
      st.id = tileId(p.identity, true);
      st.className = "tile screen has-video";
      st.innerHTML = '<div class="label"><span class="nm"></span></div>';
      st.querySelector(".nm")!.textContent = `${p.name || p.identity} (screen)`;
      tile.after(st);
    }
    st.prepend(media);
  } else if (media instanceof HTMLVideoElement) {
    media.playsInline = true;
    tile.prepend(media);
  } else {
    media.muted = tile.classList.contains("muted-audio");
    tile.append(media);
  }
  render();
}

/** Leaves the room by going back to the join card's history entry (popstate disconnects). */
function leave(): void {
  if ((history.state as { inRoom?: boolean } | null)?.inRoom) history.back();
  else void room?.disconnect();
}

window.addEventListener("popstate", () => void room?.disconnect());
