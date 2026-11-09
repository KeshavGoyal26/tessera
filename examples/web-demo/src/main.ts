import { ConnectionQuality, ConnectionState, Room, RoomEvent, TrackSource } from "@tessera/client";
import type { Participant, RemoteParticipant, RemoteTrack, RemoteTrackPublication } from "@tessera/client";
import { leftMessage } from "./reasons";

function el<T extends HTMLElement>(id: string): T {
  const node = document.getElementById(id);
  if (!node) throw new Error(`missing #${id}`);
  return node as T;
}

const landing = el<HTMLElement>("landing");
const callEl = el<HTMLElement>("call");
const form = el<HTMLFormElement>("join");
const grid = el<HTMLElement>("grid");
const statusEl = el<HTMLElement>("status");
const errorEl = el<HTMLElement>("error");
const toastEl = el<HTMLElement>("toast");
const roomLabel = el<HTMLElement>("room-name");
const micButton = el<HTMLButtonElement>("mic");
const camButton = el<HTMLButtonElement>("cam");
const screenButton = el<HTMLButtonElement>("screen");
const leaveButton = el<HTMLButtonElement>("leave");
const joinButton = el<HTMLButtonElement>("join-button");
const inviteButton = el<HTMLButtonElement>("invite");
const statsToggle = el<HTMLButtonElement>("stats-toggle");
const statsEl = el<HTMLElement>("stats");
const advanced = el<HTMLDetailsElement>("advanced");

let room: Room | undefined;
let statsTimer: number | undefined;
let refreshTimer: number | undefined;
let currentRoomName = "";

const field = (name: string) => form.elements.namedItem(name) as HTMLInputElement;

// ?room=&name=&url=&token= prefill the form; ?autojoin=1 submits it.
const params = new URLSearchParams(location.search);
for (const key of ["name", "room", "url", "token"] as const) {
  const v = params.get(key);
  if (v) field(key).value = v;
}
if (params.get("url") || params.get("token")) advanced.open = true;
if (params.get("record") === "1") field("record").checked = true;
try {
  const saved = localStorage.getItem("tessera-demo-name");
  if (saved && !field("name").value) field("name").value = saved;
} catch {
  /* storage blocked */
}

function showError(err: unknown): void {
  errorEl.textContent = err instanceof Error ? err.message : String(err);
  errorEl.hidden = false;
}

let toastTimer: number | undefined;
function toast(msg: string): void {
  toastEl.textContent = msg;
  toastEl.hidden = false;
  window.clearTimeout(toastTimer);
  toastTimer = window.setTimeout(() => (toastEl.hidden = true), 2500);
}

function setStatus(state: ConnectionState): void {
  statusEl.textContent = state;
  statusEl.dataset.state = state;
  const connected = state === ConnectionState.Connected;
  for (const b of [micButton, camButton, screenButton, leaveButton]) b.disabled = !connected;
  joinButton.disabled = state !== ConnectionState.Disconnected;
}

const MIC_ICON =
  '<path d="M12 14a3 3 0 0 0 3-3V6a3 3 0 0 0-6 0v5a3 3 0 0 0 3 3Zm5-3a5 5 0 0 1-10 0H5a7 7 0 0 0 6 6.9V21h2v-3.1A7 7 0 0 0 19 11h-2Z"/><path d="M4 4l16 16" stroke="currentColor" stroke-width="2.4" fill="none"/>';
const CAM_ICON =
  '<path d="M15 8v8H5V8h10Zm2 3 4-3v8l-4-3v-2Z"/><path d="M4 4l16 16" stroke="currentColor" stroke-width="2.4" fill="none"/>';

function tileFor(participant: Participant, screen = false): HTMLElement {
  const id = `tile-${participant.identity}${screen ? "-screen" : ""}`;
  let tile = document.getElementById(id);
  if (tile) return tile;
  tile = document.createElement("div");
  tile.id = id;
  tile.className = "tile";
  tile.dataset.identity = participant.identity;
  if (participant === room?.localParticipant) tile.classList.add("self");
  if (screen) tile.classList.add("screen", "has-video");
  const display = participant.name || participant.identity;

  const avatar = document.createElement("div");
  avatar.className = "avatar";
  avatar.textContent = display.slice(0, 1).toUpperCase();

  const label = document.createElement("div");
  label.className = "label";
  const nm = document.createElement("span");
  nm.className = "nm";
  nm.textContent = display + (screen ? " (screen)" : "") + (tile.classList.contains("self") ? " (you)" : "");
  label.append(nm);
  if (!screen) {
    label.insertAdjacentHTML("beforeend", `<svg class="ico mic off" viewBox="0 0 24 24">${MIC_ICON}</svg>`);
    label.insertAdjacentHTML("beforeend", `<svg class="ico cam off" viewBox="0 0 24 24">${CAM_ICON}</svg>`);
  }
  tile.append(avatar, label);
  if (!screen) {
    const rec = document.createElement("div");
    rec.className = "rec";
    rec.title = "This participant is being recorded";
    rec.textContent = "REC";
    tile.append(rec);
    const badge = document.createElement("div");
    badge.className = "badge";
    badge.dataset.q = "3";
    badge.title = "connection quality";
    badge.innerHTML = "<i></i><i></i><i></i>";
    badge.hidden = true;
    tile.append(badge);
  }
  grid.append(tile);
  return tile;
}

// refresh derives mic/camera state and the video/avatar swap from SDK state once a second.
function refresh(): void {
  if (!room) return;
  for (const p of [room.localParticipant, ...room.remoteParticipants.values()]) {
    const tile = tileFor(p);
    const mic = p.getTrackPublication(TrackSource.Microphone);
    const cam = p.getTrackPublication(TrackSource.Camera);
    tile.classList.toggle("mic-off", !mic || mic.isMuted);
    tile.classList.toggle("cam-off", !cam || cam.isMuted);
    tile.classList.toggle("has-video", !!tile.querySelector("video") && !!cam && !cam.isMuted);
    tile.classList.toggle("recorded", p.attributes?.recorded === "true");
  }
}

function onTrackSubscribed(track: RemoteTrack, pub: RemoteTrackPublication, participant: RemoteParticipant) {
  const media = track.attach();
  const screen = pub.source === TrackSource.ScreenShare;
  const tile = tileFor(participant, screen);
  if (media instanceof HTMLVideoElement) {
    media.playsInline = true;
    tile.prepend(media);
  } else {
    tile.append(media); // audio element
  }
  refresh();
}

function onTrackUnsubscribed(track: RemoteTrack, _pub: RemoteTrackPublication, participant: RemoteParticipant) {
  for (const media of track.detach()) media.remove();
  if (track.source === TrackSource.ScreenShare) document.getElementById(`tile-${participant.identity}-screen`)?.remove();
  refresh();
}

function qualityLevel(q: ConnectionQuality): string {
  switch (q) {
    case ConnectionQuality.EXCELLENT:
      return "3";
    case ConnectionQuality.GOOD:
      return "2";
    case ConnectionQuality.POOR:
      return "1";
    default:
      return "0";
  }
}

function wire(r: Room): void {
  r.on(RoomEvent.TrackSubscribed, onTrackSubscribed)
    .on(RoomEvent.TrackUnsubscribed, onTrackUnsubscribed)
    .on(RoomEvent.ParticipantConnected, (p) => {
      tileFor(p);
      refresh();
    })
    .on(RoomEvent.ParticipantDisconnected, (p) => {
      for (const t of grid.querySelectorAll(`[data-identity="${CSS.escape(p.identity)}"]`)) t.remove();
    })
    .on(RoomEvent.TrackMuted, refresh)
    .on(RoomEvent.TrackUnmuted, refresh)
    .on(RoomEvent.TrackStreamStateChanged, (pub, active, p) => {
      tileFor(p, pub.source === TrackSource.ScreenShare).classList.toggle("paused", !active);
    })
    .on(RoomEvent.ConnectionQualityChanged, (q, p) => {
      const badge = tileFor(p).querySelector<HTMLElement>(".badge");
      if (badge) {
        badge.dataset.q = qualityLevel(q);
        badge.hidden = q === ConnectionQuality.UNSPECIFIED;
      }
    })
    .on(RoomEvent.ActiveSpeakersChanged, (speakers) => {
      const ids = new Set(speakers.map((p) => `tile-${p.identity}`));
      for (const tile of grid.querySelectorAll<HTMLElement>(".tile:not(.screen)")) {
        tile.classList.toggle("speaking", ids.has(tile.id));
      }
    })
    .on(RoomEvent.LocalTrackUnpublished, (pub) => {
      if (pub.source === TrackSource.ScreenShare) {
        document.getElementById(`tile-${r.localParticipant.identity}-screen`)?.remove();
        screenButton.setAttribute("aria-pressed", "false");
      }
    })
    .on(RoomEvent.Reconnecting, () => setStatus(ConnectionState.Reconnecting))
    .on(RoomEvent.Reconnected, () => setStatus(ConnectionState.Connected))
    .on(RoomEvent.Disconnected, (reason) => {
      const why = leftMessage(reason);
      if (why) showError(new Error(why));
      grid.replaceChildren();
      room = undefined;
      for (const b of [micButton, camButton, screenButton]) b.setAttribute("aria-pressed", "false");
      stopStats();
      statsEl.hidden = true;
      statsToggle.setAttribute("aria-pressed", "false");
      window.clearInterval(refreshTimer);
      setStatus(ConnectionState.Disconnected);
      callEl.hidden = true;
      landing.hidden = false;
    });
}

// Server and token: explicit values win; otherwise ask the dev server on the same host.
async function resolveConnection(roomName: string, name: string): Promise<{ url: string; token: string }> {
  const host = location.hostname;
  const url = field("url").value.trim() || `ws://${host}:7880`;
  const manual = field("token").value.trim();
  if (manual) return { url, token: manual };
  const http = url.replace(/^ws/, "http").replace(/\/rtc.*$/, "").replace(/\/$/, "");
  const identity = `${name.toLowerCase().replace(/[^a-z0-9]+/g, "-") || "guest"}-${Math.random().toString(36).slice(2, 6)}`;
  const q = new URLSearchParams({ room: roomName, identity, name });
  if (field("record").checked) q.set("record", "1");
  let res: Response;
  try {
    res = await fetch(`${http}/dev/token?${q}`);
  } catch {
    throw new Error(`Cannot reach ${http}. Is the server running (tessera --dev)? Or paste a server URL and token under Advanced.`);
  }
  if (res.status === 404) throw new Error("This server has no /dev/token (it is not running with --dev). Paste a token under Advanced.");
  if (!res.ok) throw new Error(`Token request failed: ${res.status} ${await res.text()}`);
  return { url, token: ((await res.json()) as { token: string }).token };
}

async function join(): Promise<void> {
  errorEl.hidden = true;
  const hasToken = field("token").value.trim() !== "";
  const name = field("name").value.trim() || (hasToken ? "guest" : "");
  const roomName = field("room").value.trim() || (hasToken ? "room" : ""); // the token decides the room
  if (!name || !roomName) return;
  try {
    localStorage.setItem("tessera-demo-name", name);
  } catch {
    /* storage blocked */
  }
  setStatus(ConnectionState.Connecting);
  const r = new Room();
  wire(r);
  try {
    const { url, token } = await resolveConnection(roomName, name);
    await r.connect(url, token);
    room = r;
    (window as unknown as { room: Room }).room = r; // handy for debugging and E2E tests
    currentRoomName = roomName;
    roomLabel.textContent = roomName;
    landing.hidden = true;
    callEl.hidden = false;
    tileFor(r.localParticipant);
    for (const p of r.remoteParticipants.values()) tileFor(p);
    setStatus(r.state);
    refresh();
    refreshTimer = window.setInterval(refresh, 1000);
  } catch (err) {
    r.removeAllListeners();
    setStatus(ConnectionState.Disconnected);
    showError(err);
  }
}

form.addEventListener("submit", (event) => {
  event.preventDefault();
  void join();
});

inviteButton.addEventListener("click", async () => {
  const link = new URL(location.href);
  const q = new URLSearchParams({ room: currentRoomName });
  const server = new URLSearchParams(location.search).get("url");
  if (server) q.set("url", server); // the invitee must reach the same server
  link.search = q.toString();
  try {
    await navigator.clipboard.writeText(link.toString());
    toast("Invite link copied");
  } catch {
    toast(link.toString());
  }
});

micButton.addEventListener("click", async () => {
  try {
    const on = micButton.getAttribute("aria-pressed") !== "true";
    await room?.localParticipant.setMicrophoneEnabled(on);
    micButton.setAttribute("aria-pressed", String(on));
    refresh();
  } catch (err) {
    toast(err instanceof Error ? err.message : String(err));
  }
});

camButton.addEventListener("click", async () => {
  try {
    if (!room) return;
    const on = camButton.getAttribute("aria-pressed") !== "true";
    const pub = await room.localParticipant.setCameraEnabled(on);
    camButton.setAttribute("aria-pressed", String(on));
    if (pub && on && pub.track.attachedElements.length === 0) {
      tileFor(room.localParticipant).prepend(pub.track.attach());
    }
    refresh();
  } catch (err) {
    toast(err instanceof Error ? err.message : String(err));
  }
});

screenButton.addEventListener("click", async () => {
  try {
    if (!room) return;
    const on = screenButton.getAttribute("aria-pressed") !== "true";
    const pub = await room.localParticipant.setScreenShareEnabled(on);
    screenButton.setAttribute("aria-pressed", String(on));
    if (on && pub) tileFor(room.localParticipant, true).prepend(pub.track.attach());
    if (!on) document.getElementById(`tile-${room.localParticipant.identity}-screen`)?.remove();
  } catch (err) {
    toast(err instanceof Error ? err.message : String(err));
  }
});

function stopStats(): void {
  window.clearInterval(statsTimer);
  statsTimer = undefined;
}

const kbps = (bps: number) => `${Math.round(bps / 1000)} kbps`;

statsToggle.addEventListener("click", () => {
  const on = statsEl.hidden;
  statsEl.hidden = !on;
  statsToggle.setAttribute("aria-pressed", String(on));
  stopStats();
  if (!on) return;
  const tick = async () => {
    if (!room) return;
    const s = await room.getStats();
    const pub = s.publisher;
    const sub = s.subscriber;
    statsEl.textContent = [
      `RTT      signal ${Math.round(s.signalRttMs)} ms  media ${Math.round(pub?.rttMs ?? sub?.rttMs ?? 0)} ms`,
      `send     ${kbps(pub?.send.bitrate ?? 0)}  ${pub?.send.width ?? 0}x${pub?.send.height ?? 0} @ ${Math.round(pub?.send.fps ?? 0)} fps`,
      `receive  ${kbps(sub?.receive.bitrate ?? 0)}  ${sub?.receive.width ?? 0}x${sub?.receive.height ?? 0} @ ${Math.round(sub?.receive.fps ?? 0)} fps`,
      `loss     ${(sub?.receive.lossPercent ?? 0).toFixed(1)} %  jitter ${(sub?.receive.jitterMs ?? 0).toFixed(1)} ms`,
    ].join("\n");
  };
  void tick();
  statsTimer = window.setInterval(() => void tick(), 1000);
});

leaveButton.addEventListener("click", () => {
  void room?.disconnect();
});

if (params.get("autojoin") === "1") void join();
