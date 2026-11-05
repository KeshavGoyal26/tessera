// Presenter / viewer demo: presenters publish to the server and receive nothing;
// viewers are hidden, see every presenter and subscribe only to the ones they watch.
import { ConnectionState, Room, RoomEvent, TrackSource } from "@tessera/client";
import type { RemoteParticipant, RemoteTrack, RemoteTrackPublication } from "@tessera/client";

type Role = "presenter" | "viewer";

const $ = <T extends HTMLElement>(id: string): T => {
  const node = document.getElementById(id);
  if (!node) throw new Error(`missing #${id}`);
  return node as T;
};
const form = $<HTMLFormElement>("join");
const field = (name: string) => form.elements.namedItem(name) as HTMLInputElement;
const joinButton = $<HTMLButtonElement>("join-button");
const errorEl = $<HTMLParagraphElement>("error");
const toastEl = $<HTMLParagraphElement>("toast");

let room: Room | undefined;
let role: Role = "presenter";
let toastTimer: number | undefined;

function toast(msg: string): void {
  toastEl.textContent = msg;
  toastEl.hidden = false;
  window.clearTimeout(toastTimer);
  toastTimer = window.setTimeout(() => (toastEl.hidden = true), 2500);
}
const errText = (err: unknown) => (err instanceof Error ? err.message : String(err));

// ---- landing ---------------------------------------------------------------

const params = new URLSearchParams(location.search);
for (const key of ["name", "room", "url"] as const) {
  const v = params.get(key);
  if (v) field(key).value = v;
}
if (params.get("role") === "viewer") (form.querySelector('input[value="viewer"]') as HTMLInputElement).checked = true;
if (params.get("record") === "1") field("record").checked = true;
try {
  const saved = localStorage.getItem("tessera-demo-name");
  if (saved && !field("name").value) field("name").value = saved;
} catch {
  /* storage blocked */
}

function selectedRole(): Role {
  return (form.querySelector<HTMLInputElement>('input[name="role"]:checked')?.value as Role) ?? "presenter";
}
function syncLanding(): void {
  const r = selectedRole();
  $("record-row").hidden = r !== "presenter";
  joinButton.textContent = r === "presenter" ? "Go live" : "Start watching";
}
form.addEventListener("change", syncLanding);
syncLanding();

async function fetchToken(url: string, roomName: string, name: string, r: Role, record: boolean): Promise<string> {
  const http = url.replace(/^ws/, "http").replace(/\/rtc.*$/, "").replace(/\/$/, "");
  const identity = `${r === "viewer" ? "v" : "p"}-${name.toLowerCase().replace(/[^a-z0-9]+/g, "-") || "guest"}-${Math.random().toString(36).slice(2, 6)}`;
  const q = new URLSearchParams({ room: roomName, identity, name, role: r });
  if (record) q.set("record", "1");
  let res: Response;
  try {
    res = await fetch(`${http}/dev/token?${q}`);
  } catch {
    throw new Error(`Cannot reach ${http}. Is the server running with --dev?`);
  }
  if (res.status === 404) throw new Error("This server has no /dev/token (start it with --dev).");
  if (!res.ok) throw new Error(`Token request failed: ${res.status} ${await res.text()}`);
  return ((await res.json()) as { token: string }).token;
}

form.addEventListener("submit", (e) => {
  e.preventDefault();
  void join();
});

async function join(): Promise<void> {
  errorEl.hidden = true;
  const name = field("name").value.trim();
  const roomName = field("room").value.trim();
  if (!name || !roomName) return;
  try {
    localStorage.setItem("tessera-demo-name", name);
  } catch {
    /* storage blocked */
  }
  role = selectedRole();
  joinButton.disabled = true;
  // Viewers subscribe only to what they choose; presenters cannot subscribe at all.
  const r = new Room({ adaptiveStream: true, dynacast: true, autoSubscribe: false });
  try {
    const url = field("url").value.trim() || `ws://${location.hostname}:7880`;
    const token = await fetchToken(url, roomName, name, role, role === "presenter" && field("record").checked);
    await r.connect(url, token);
    room = r;
    (window as unknown as { room: Room }).room = r;
    for (const n of document.querySelectorAll("[data-room]")) n.textContent = roomName;
    $("landing").hidden = true;
    r.on(RoomEvent.Reconnecting, () => setStatus(ConnectionState.Reconnecting))
      .on(RoomEvent.Reconnected, () => setStatus(ConnectionState.Connected))
      .on(RoomEvent.Disconnected, () => backToLanding());
    setStatus(ConnectionState.Connected);
    if (role === "presenter") await startPresenting(r, name);
    else startWatching(r);
  } catch (err) {
    r.removeAllListeners();
    void r.disconnect().catch(() => undefined);
    errorEl.textContent = errText(err);
    errorEl.hidden = false;
  } finally {
    joinButton.disabled = false;
  }
}

function setStatus(state: ConnectionState): void {
  for (const n of document.querySelectorAll<HTMLElement>("[data-status]")) {
    n.textContent = state;
    n.dataset.state = state;
  }
}

function backToLanding(): void {
  room?.removeAllListeners();
  room = undefined;
  watched.clear();
  $("present").hidden = true;
  $("watch").hidden = true;
  $("landing").hidden = false;
  $("presenter-list").replaceChildren();
  for (const t of document.querySelectorAll("#watch-stage .tile")) t.remove();
  for (const v of document.querySelectorAll("#preview video")) v.remove();
  setStatus(ConnectionState.Disconnected);
}

// ---- presenter -------------------------------------------------------------

async function startPresenting(r: Room, name: string): Promise<void> {
  $("present").hidden = false;
  $("preview-name").textContent = `${name} (you)`;
  $("preview-avatar").textContent = name.slice(0, 1).toUpperCase();
  $("preview").classList.toggle("recorded", r.localParticipant.attributes?.recorded === "true");
  // Go live with camera and mic; either may be refused, presenting continues with what works.
  await setCam(true).catch((err) => toast(`Camera: ${errText(err)}`));
  await setMic(true).catch((err) => toast(`Microphone: ${errText(err)}`));
}

async function setMic(on: boolean): Promise<void> {
  await room?.localParticipant.setMicrophoneEnabled(on);
  $("p-mic").setAttribute("aria-pressed", String(on));
}

async function setCam(on: boolean): Promise<void> {
  if (!room) return;
  const pub = await room.localParticipant.setCameraEnabled(on);
  $("p-cam").setAttribute("aria-pressed", String(on));
  const preview = $("preview");
  preview.querySelector("video:not(.screen)")?.remove();
  if (on && pub) {
    const v = pub.track.attach() as HTMLVideoElement;
    v.muted = true;
    preview.prepend(v);
  }
  preview.classList.toggle("has-video", on);
  $("preview-hint").hidden = on;
}

$("p-mic").addEventListener("click", () => {
  void setMic($("p-mic").getAttribute("aria-pressed") !== "true").catch((err) => toast(errText(err)));
});
$("p-cam").addEventListener("click", () => {
  void setCam($("p-cam").getAttribute("aria-pressed") !== "true").catch((err) => toast(errText(err)));
});
$("p-screen").addEventListener("click", async () => {
  if (!room) return;
  const btn = $("p-screen");
  const on = btn.getAttribute("aria-pressed") !== "true";
  try {
    const pub = await room.localParticipant.setScreenShareEnabled(on);
    btn.setAttribute("aria-pressed", String(on));
    const stage = $("present-stage");
    document.getElementById("preview-screen")?.remove();
    if (on && pub) {
      const tile = document.createElement("div");
      tile.className = "tile screen has-video";
      tile.id = "preview-screen";
      tile.append(pub.track.attach());
      tile.insertAdjacentHTML("beforeend", '<div class="label"><span class="nm">Your screen</span></div>');
      stage.append(tile);
    }
    stage.dataset.count = String(stage.querySelectorAll(".tile").length);
  } catch (err) {
    toast(errText(err));
  }
});
$("p-leave").addEventListener("click", () => void room?.disconnect());

// ---- viewer ----------------------------------------------------------------

/** Identities the viewer is watching. Subscriptions follow this set. */
const watched = new Set<string>();

function isPresenter(p: RemoteParticipant): boolean {
  // Presenters announce themselves; anyone else who publishes (e.g. call participants in the same room) also counts.
  return p.attributes?.role === "presenter" || p.trackPublications.size > 0;
}

function presenters(): RemoteParticipant[] {
  return [...(room?.remoteParticipants.values() ?? [])].filter(isPresenter).sort((a, b) => (a.name || a.identity).localeCompare(b.name || b.identity));
}

function startWatching(r: Room): void {
  $("watch").hidden = false;
  r.on(RoomEvent.ParticipantConnected, renderList)
    .on(RoomEvent.ParticipantDisconnected, (p) => {
      unwatch(p.identity);
      renderList();
    })
    .on(RoomEvent.TrackPublished, (pub, p) => {
      if (watched.has(p.identity)) pub.setSubscribed(true);
      renderList();
    })
    .on(RoomEvent.TrackUnpublished, (_pub, p) => {
      renderList();
      if (watched.has(p.identity)) syncTile(p);
    })
    .on(RoomEvent.TrackMuted, () => renderList())
    .on(RoomEvent.TrackUnmuted, () => renderList())
    .on(RoomEvent.TrackSubscribed, onSubscribed)
    .on(RoomEvent.TrackUnsubscribed, (track) => {
      for (const el of track.detach()) el.remove();
    });
  renderList();
  // With a single presenter there is nothing to choose: start watching right away.
  const ps = presenters();
  const only = ps[0];
  if (ps.length === 1 && only) watch(only);
}

const ICON = {
  cam: '<svg class="ico" viewBox="0 0 24 24"><path d="M15 8v8H5V8h10Zm2 3 4-3v8l-4-3v-2Z"/></svg>',
  mic: '<svg class="ico" viewBox="0 0 24 24"><path d="M12 14a3 3 0 0 0 3-3V6a3 3 0 0 0-6 0v5a3 3 0 0 0 3 3Zm5-3a5 5 0 0 1-10 0H5a7 7 0 0 0 6 6.9V21h2v-3.1A7 7 0 0 0 19 11h-2Z"/></svg>',
  screen: '<svg class="ico" viewBox="0 0 24 24"><path d="M3 5h18v11H3V5Zm2 2v7h14V7H5Zm3 11h8v2H8v-2Z"/></svg>',
};

function live(p: RemoteParticipant, source: TrackSource): boolean {
  const pub = p.getTrackPublication(source);
  return !!pub && !pub.isMuted;
}

function renderList(): void {
  const list = $("presenter-list");
  const ps = presenters();
  $("no-presenters").hidden = ps.length > 0;
  $("live-count").textContent = `${ps.length} live`;
  list.replaceChildren(
    ...ps.map((p) => {
      const li = document.createElement("li");
      const on = watched.has(p.identity);
      li.className = on ? "watching" : "";
      const btn = document.createElement("button");
      btn.type = "button";
      btn.className = "presenter";
      btn.setAttribute("aria-pressed", String(on));
      btn.title = on ? "Stop watching" : "Watch";
      const name = p.name || p.identity;
      const media = [
        live(p, TrackSource.Camera) ? ICON.cam : "",
        live(p, TrackSource.Microphone) ? ICON.mic : "",
        live(p, TrackSource.ScreenShare) ? ICON.screen : "",
      ].join("");
      btn.innerHTML = `<span class="av"></span><span class="who"><span class="pn"></span><span class="media">${media || '<span class="muted">no media yet</span>'}</span></span><span class="go">${on ? "Watching" : "Watch"}</span>`;
      btn.querySelector(".av")!.textContent = name.slice(0, 1).toUpperCase();
      btn.querySelector(".pn")!.textContent = name;
      if (p.attributes?.recorded === "true") btn.querySelector(".pn")!.insertAdjacentHTML("afterend", '<span class="rec-dot" title="Recorded">REC</span>');
      btn.addEventListener("click", () => (watched.has(p.identity) ? unwatch(p.identity) : watch(p)));
      li.append(btn);
      return li;
    }),
  );
  // Drop watched presenters that are gone.
  const stale = [...watched].filter((id) => !ps.some((p) => p.identity === id));
  for (const id of stale) unwatch(id);
  layoutStage();
}

function watch(p: RemoteParticipant): void {
  watched.add(p.identity);
  for (const pub of p.trackPublications.values()) pub.setSubscribed(true);
  syncTile(p);
  renderList();
}

function unwatch(identity: string): void {
  if (!watched.delete(identity)) return;
  const p = room?.remoteParticipants.get(identity) ?? [...(room?.remoteParticipants.values() ?? [])].find((x) => x.identity === identity);
  if (p) for (const pub of p.trackPublications.values()) pub.setSubscribed(false);
  document.getElementById(tileId(identity))?.remove();
  document.getElementById(tileId(identity, true))?.remove();
  renderList();
}

$("watch-all").addEventListener("click", () => {
  for (const p of presenters()) if (!watched.has(p.identity)) watch(p);
});
$("watch-none").addEventListener("click", () => {
  for (const id of [...watched]) unwatch(id);
});
$("w-leave").addEventListener("click", () => void room?.disconnect());

const tileId = (identity: string, screen = false) => `w-${identity}${screen ? "-screen" : ""}`;

/** Creates the presenter's tile (and a screen tile while they share) and keeps its state current. */
function syncTile(p: RemoteParticipant): HTMLElement {
  const stage = $("watch-stage");
  let tile = document.getElementById(tileId(p.identity));
  const name = p.name || p.identity;
  if (!tile) {
    tile = document.createElement("div");
    tile.id = tileId(p.identity);
    tile.className = "tile";
    tile.innerHTML = `<div class="avatar"></div><div class="label"><span class="nm"></span></div>
      <div class="tile-actions"><button class="icon-btn" data-act="audio" type="button" aria-pressed="true" title="Mute this presenter">🔊</button><button class="icon-btn" data-act="close" type="button" title="Stop watching">✕</button></div>`;
    tile.querySelector(".avatar")!.textContent = name.slice(0, 1).toUpperCase();
    tile.querySelector(".nm")!.textContent = name;
    tile.querySelector('[data-act="close"]')!.addEventListener("click", () => unwatch(p.identity));
    tile.querySelector('[data-act="audio"]')!.addEventListener("click", (e) => {
      const b = e.currentTarget as HTMLButtonElement;
      const on = b.getAttribute("aria-pressed") !== "true";
      b.setAttribute("aria-pressed", String(on));
      b.textContent = on ? "🔊" : "🔇";
      b.title = on ? "Mute this presenter" : "Unmute this presenter";
      for (const a of tile!.querySelectorAll("audio")) a.muted = !on;
    });
    stage.append(tile);
  }
  tile.classList.toggle("has-video", !!tile.querySelector("video") && live(p, TrackSource.Camera));
  if (!live(p, TrackSource.ScreenShare)) document.getElementById(tileId(p.identity, true))?.remove();
  layoutStage();
  return tile;
}

function onSubscribed(track: RemoteTrack, pub: RemoteTrackPublication, p: RemoteParticipant): void {
  if (!watched.has(p.identity)) {
    pub.setSubscribed(false);
    return;
  }
  const tile = syncTile(p);
  const el = track.attach();
  if (pub.source === TrackSource.ScreenShare) {
    let st = document.getElementById(tileId(p.identity, true));
    if (!st) {
      st = document.createElement("div");
      st.id = tileId(p.identity, true);
      st.className = "tile screen has-video";
      st.innerHTML = '<div class="label"><span class="nm"></span></div>';
      st.querySelector(".nm")!.textContent = `${p.name || p.identity} (screen)`;
      tile.after(st);
    }
    st.prepend(el);
  } else if (el instanceof HTMLVideoElement) {
    el.playsInline = true;
    tile.prepend(el);
  } else {
    el.muted = tile.querySelector('[data-act="audio"]')?.getAttribute("aria-pressed") === "false";
    tile.append(el);
  }
  syncTile(p);
}

function layoutStage(): void {
  const stage = $("watch-stage");
  const n = stage.querySelectorAll(".tile").length;
  stage.dataset.count = String(Math.min(n, 5));
  $("stage-empty").hidden = n > 0;
  $("stage-empty").textContent = presenters().length ? "Choose a presenter to start watching." : "Waiting for someone to go live.";
}
