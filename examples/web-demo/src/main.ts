import { ConnectionQuality, ConnectionState, Room, RoomEvent, TrackSource, VideoQuality } from "@tessera/client";
import type { Participant, RemoteParticipant, RemoteTrack, RemoteTrackPublication } from "@tessera/client";

function el<T extends HTMLElement>(id: string): T {
  const node = document.getElementById(id);
  if (!node) throw new Error(`missing #${id}`);
  return node as T;
}

const form = el<HTMLFormElement>("join");
const grid = el<HTMLElement>("grid");
const statusEl = el<HTMLElement>("status");
const errorEl = el<HTMLElement>("error");
const micButton = el<HTMLButtonElement>("mic");
const camButton = el<HTMLButtonElement>("cam");
const screenButton = el<HTMLButtonElement>("screen");
const leaveButton = el<HTMLButtonElement>("leave");
const joinButton = el<HTMLButtonElement>("join-button");
const micDevice = el<HTMLSelectElement>("mic-device");
const camDevice = el<HTMLSelectElement>("cam-device");
const qualitySelect = el<HTMLSelectElement>("quality");
const statsToggle = el<HTMLButtonElement>("stats-toggle");
const statsEl = el<HTMLElement>("stats");

let room: Room | undefined;
let statsTimer: number | undefined;

// ?url=...&token=... prefills (and ?autojoin=1 submits) the join form.
const params = new URLSearchParams(location.search);
for (const key of ["url", "token"] as const) {
  const v = params.get(key);
  const input = form.elements.namedItem(key) as HTMLInputElement | null;
  if (v && input) input.value = v;
}

function showError(err: unknown): void {
  errorEl.textContent = err instanceof Error ? `${err.name}: ${err.message}` : String(err);
  errorEl.hidden = false;
}

function setStatus(state: ConnectionState): void {
  statusEl.textContent = state;
  statusEl.dataset.state = state;
  const connected = state === ConnectionState.Connected;
  for (const b of [micButton, camButton, screenButton, leaveButton, micDevice, camDevice, qualitySelect]) {
    b.disabled = !connected;
  }
  joinButton.disabled = state !== ConnectionState.Disconnected;
}

function tileFor(participant: Participant, suffix = ""): HTMLElement {
  const id = `tile-${participant.identity}${suffix}`;
  let tile = document.getElementById(id);
  if (!tile) {
    tile = document.createElement("div");
    tile.id = id;
    tile.className = "tile";
    tile.dataset.identity = participant.identity;
    const name = document.createElement("span");
    name.className = "name";
    name.textContent = (participant.name || participant.identity) + (suffix ? " (screen)" : "");
    const badge = document.createElement("span");
    badge.className = "badge";
    badge.hidden = true;
    tile.append(name, badge);
    grid.append(tile);
  }
  return tile;
}

function tileSuffix(source: TrackSource): string {
  return source === TrackSource.ScreenShare ? "-screen" : "";
}

function attach(tile: HTMLElement, media: HTMLMediaElement, source: TrackSource): void {
  if (source === TrackSource.ScreenShare) tile.classList.add("screen");
  tile.prepend(media);
}

function onTrackSubscribed(track: RemoteTrack, pub: RemoteTrackPublication, participant: RemoteParticipant) {
  const media = track.attach();
  media.dataset.sid = track.sid;
  attach(tileFor(participant, tileSuffix(pub.source)), media, pub.source);
}

function onTrackUnsubscribed(track: RemoteTrack, _pub: RemoteTrackPublication, participant: RemoteParticipant) {
  for (const media of track.detach()) media.remove();
  const tile = document.getElementById(`tile-${participant.identity}${tileSuffix(track.source)}`);
  if (tile && track.source === TrackSource.ScreenShare) tile.remove();
}

function setMuted(participant: Participant, source: TrackSource, muted: boolean): void {
  if (source !== TrackSource.Camera && source !== TrackSource.Microphone) return;
  tileFor(participant).classList.toggle("muted", muted && source === TrackSource.Microphone);
}

const qualityIcon: Record<number, string> = {
  [ConnectionQuality.EXCELLENT]: "●●●",
  [ConnectionQuality.GOOD]: "●●○",
  [ConnectionQuality.POOR]: "●○○",
  [ConnectionQuality.LOST]: "○○○",
};

function wire(r: Room): void {
  for (const ev of [RoomEvent.Reconnecting, RoomEvent.Reconnected, RoomEvent.Disconnected, RoomEvent.TrackSubscribed, RoomEvent.TrackUnsubscribed, RoomEvent.ParticipantConnected, RoomEvent.ParticipantDisconnected]) {
    r.on(ev, ((...args: unknown[]) => console.debug("[tessera]", ev, args.length)) as never);
  }
  r.on(RoomEvent.TrackSubscribed, onTrackSubscribed)
    .on(RoomEvent.TrackUnsubscribed, onTrackUnsubscribed)
    .on(RoomEvent.ParticipantConnected, (p) => void tileFor(p))
    .on(RoomEvent.ParticipantDisconnected, (p) => {
      for (const t of grid.querySelectorAll(`[data-identity="${CSS.escape(p.identity)}"]`)) t.remove();
    })
    .on(RoomEvent.TrackMuted, (pub, p) => setMuted(p, pub.source, true))
    .on(RoomEvent.TrackUnmuted, (pub, p) => setMuted(p, pub.source, false))
    .on(RoomEvent.TrackStreamStateChanged, (pub, active, p) => {
      tileFor(p, tileSuffix(pub.source)).classList.toggle("paused", !active);
    })
    .on(RoomEvent.ConnectionQualityChanged, (q, p) => {
      const badge = tileFor(p).querySelector<HTMLElement>(".badge");
      if (badge) {
        badge.textContent = qualityIcon[q] ?? "";
        badge.hidden = !badge.textContent;
      }
    })
    .on(RoomEvent.ActiveSpeakersChanged, (speakers) => {
      const ids = new Set(speakers.map((p) => `tile-${p.identity}`));
      for (const tile of grid.querySelectorAll<HTMLElement>(".tile")) {
        tile.classList.toggle("speaking", ids.has(tile.id));
      }
    })
    .on(RoomEvent.LocalTrackUnpublished, (pub) => {
      if (pub.source === TrackSource.ScreenShare) {
        document.getElementById(`tile-${r.localParticipant.identity}-screen`)?.remove();
        screenButton.textContent = "Share screen";
      }
    })
    .on(RoomEvent.Reconnecting, () => setStatus(ConnectionState.Reconnecting))
    .on(RoomEvent.Reconnected, () => setStatus(ConnectionState.Connected))
    .on(RoomEvent.Disconnected, () => {
      grid.replaceChildren();
      room = undefined;
      micButton.textContent = "Unmute mic";
      camButton.textContent = "Start camera";
      screenButton.textContent = "Share screen";
      stopStats();
      setStatus(ConnectionState.Disconnected);
    });
}

async function join(): Promise<void> {
  errorEl.hidden = true;
  const data = new FormData(form);
  const r = new Room({ adaptiveStream: true, dynacast: true });
  wire(r);
  setStatus(ConnectionState.Connecting);
  try {
    await r.connect(String(data.get("url")), String(data.get("token")));
    room = r;
    (window as unknown as { room: Room }).room = r; // handy for debugging and E2E tests
    tileFor(r.localParticipant);
    for (const p of r.remoteParticipants.values()) tileFor(p);
    setStatus(r.state);
    void listDevices();
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

micButton.addEventListener("click", async () => {
  try {
    const on = micButton.textContent === "Unmute mic";
    await room?.localParticipant.setMicrophoneEnabled(on);
    micButton.textContent = on ? "Mute mic" : "Unmute mic";
    if (room) setMuted(room.localParticipant, TrackSource.Microphone, !on);
  } catch (err) {
    showError(err);
  }
});

camButton.addEventListener("click", async () => {
  try {
    if (!room) return;
    const enable = camButton.textContent === "Start camera";
    const pub = await room.localParticipant.setCameraEnabled(enable);
    camButton.textContent = enable ? "Stop camera" : "Start camera";
    if (pub && enable && pub.track.attachedElements.length === 0) {
      tileFor(room.localParticipant).prepend(pub.track.attach());
    }
    void listDevices();
  } catch (err) {
    showError(err);
  }
});

screenButton.addEventListener("click", async () => {
  try {
    if (!room) return;
    const on = screenButton.textContent === "Share screen";
    const pub = await room.localParticipant.setScreenShareEnabled(on);
    screenButton.textContent = on ? "Stop sharing" : "Share screen";
    if (on && pub) attach(tileFor(room.localParticipant, "-screen"), pub.track.attach(), TrackSource.ScreenShare);
    if (!on) document.getElementById(`tile-${room.localParticipant.identity}-screen`)?.remove();
  } catch (err) {
    showError(err);
  }
});

async function listDevices(): Promise<void> {
  try {
    const devices = await navigator.mediaDevices.enumerateDevices();
    const fill = (select: HTMLSelectElement, kind: MediaDeviceKind) => {
      const current = select.value;
      select.replaceChildren(
        ...devices
          .filter((d) => d.kind === kind)
          .map((d, i) => new Option(d.label || `${kind} ${i + 1}`, d.deviceId)),
      );
      if (current) select.value = current;
    };
    fill(micDevice, "audioinput");
    fill(camDevice, "videoinput");
  } catch {
    /* no permission yet */
  }
}

micDevice.addEventListener("change", () => {
  room?.localParticipant.switchActiveDevice("audioinput", micDevice.value).catch(showError);
});
camDevice.addEventListener("change", () => {
  room?.localParticipant.switchActiveDevice("videoinput", camDevice.value).catch(showError);
});

qualitySelect.addEventListener("change", () => {
  if (!room) return;
  const q = { low: VideoQuality.LOW, medium: VideoQuality.MEDIUM, high: VideoQuality.HIGH }[qualitySelect.value as "low"];
  for (const p of room.remoteParticipants.values()) {
    for (const pub of p.trackPublications.values()) {
      if (pub.kind === "video" && pub.isSubscribed && q !== undefined) pub.setVideoQuality(q);
    }
  }
});

function stopStats(): void {
  window.clearInterval(statsTimer);
  statsTimer = undefined;
}

function fmtKbps(bps: number): string {
  return `${Math.round(bps / 1000)} kbps`;
}

statsToggle.addEventListener("click", () => {
  const on = statsEl.hidden;
  statsEl.hidden = !on;
  statsToggle.setAttribute("aria-pressed", String(on));
  stopStats();
  if (!on) return;
  statsTimer = window.setInterval(async () => {
    if (!room) return;
    const s = await room.getStats();
    const pub = s.publisher;
    const sub = s.subscriber;
    statsEl.textContent = [
      `signal rtt  ${Math.round(s.signalRttMs)} ms   media rtt ${Math.round(pub?.rttMs ?? sub?.rttMs ?? 0)} ms`,
      `send        ${fmtKbps(pub?.send.bitrate ?? 0)}  ${pub?.send.width ?? 0}x${pub?.send.height ?? 0} @ ${Math.round(pub?.send.fps ?? 0)} fps`,
      `receive     ${fmtKbps(sub?.receive.bitrate ?? 0)}  ${sub?.receive.width ?? 0}x${sub?.receive.height ?? 0} @ ${Math.round(sub?.receive.fps ?? 0)} fps`,
      `loss        ${(sub?.receive.lossPercent ?? 0).toFixed(1)} %   jitter ${(sub?.receive.jitterMs ?? 0).toFixed(1)} ms`,
    ].join("\n");
  }, 1000);
});

leaveButton.addEventListener("click", () => {
  void room?.disconnect();
});

if (params.get("autojoin") === "1") void join();
