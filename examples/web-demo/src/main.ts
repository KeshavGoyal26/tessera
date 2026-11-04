import { ConnectionState, Room, RoomEvent } from "@tessera/client";
import type { Participant, RemoteParticipant, RemoteTrack } from "@tessera/client";

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
const leaveButton = el<HTMLButtonElement>("leave");
const joinButton = el<HTMLButtonElement>("join-button");

let room: Room | undefined;
let micOn = false;
let camOn = false;

function showError(err: unknown): void {
  errorEl.textContent = err instanceof Error ? `${err.name}: ${err.message}` : String(err);
  errorEl.hidden = false;
}

function setStatus(state: ConnectionState): void {
  statusEl.textContent = state;
  const connected = state === ConnectionState.Connected;
  micButton.disabled = camButton.disabled = leaveButton.disabled = !connected;
  joinButton.disabled = state !== ConnectionState.Disconnected;
}

function tileFor(participant: Participant): HTMLElement {
  const id = `tile-${participant.identity}`;
  let tile = document.getElementById(id);
  if (!tile) {
    tile = document.createElement("div");
    tile.id = id;
    tile.className = "tile";
    const name = document.createElement("span");
    name.className = "name";
    name.textContent = participant.name || participant.identity;
    tile.append(name);
    grid.append(tile);
  }
  return tile;
}

function onTrackSubscribed(track: RemoteTrack, _pub: unknown, participant: RemoteParticipant) {
  const media = track.attach();
  media.dataset.sid = track.sid;
  tileFor(participant).prepend(media);
}

function onTrackUnsubscribed(track: RemoteTrack) {
  for (const media of track.detach()) media.remove();
}

function wire(r: Room): void {
  r.on(RoomEvent.TrackSubscribed, onTrackSubscribed)
    .on(RoomEvent.TrackUnsubscribed, onTrackUnsubscribed)
    .on(RoomEvent.ParticipantConnected, (p) => void tileFor(p))
    .on(RoomEvent.ParticipantDisconnected, (p) => tileFor(p).remove())
    .on(RoomEvent.ActiveSpeakersChanged, (speakers) => {
      const ids = new Set(speakers.map((p) => `tile-${p.identity}`));
      for (const tile of grid.querySelectorAll<HTMLElement>(".tile")) {
        tile.classList.toggle("speaking", ids.has(tile.id));
      }
    })
    .on(RoomEvent.Reconnecting, () => setStatus(ConnectionState.Reconnecting))
    .on(RoomEvent.Reconnected, () => setStatus(ConnectionState.Connected))
    .on(RoomEvent.Disconnected, () => {
      grid.replaceChildren();
      room = undefined;
      micOn = camOn = false;
      setStatus(ConnectionState.Disconnected);
    });
}

form.addEventListener("submit", async (event) => {
  event.preventDefault();
  errorEl.hidden = true;
  const data = new FormData(form);
  const url = String(data.get("url"));
  const token = String(data.get("token"));

  const r = new Room({ adaptiveStream: true, dynacast: true });
  wire(r);
  setStatus(ConnectionState.Connecting);
  try {
    await r.connect(url, token);
    room = r;
    tileFor(r.localParticipant);
    setStatus(r.state);
  } catch (err) {
    r.removeAllListeners();
    setStatus(ConnectionState.Disconnected);
    showError(err);
  }
});

micButton.addEventListener("click", async () => {
  try {
    await room?.localParticipant.setMicrophoneEnabled(!micOn);
    micOn = !micOn;
    micButton.textContent = micOn ? "Mute mic" : "Unmute mic";
  } catch (err) {
    showError(err);
  }
});

camButton.addEventListener("click", async () => {
  try {
    if (!room) return;
    const enable = !camOn;
    const pub = await room.localParticipant.setCameraEnabled(enable);
    camOn = enable;
    camButton.textContent = camOn ? "Stop camera" : "Start camera";
    if (!pub) return;
    if (enable) tileFor(room.localParticipant).prepend(pub.track.attach());
    else for (const media of pub.track.detach()) media.remove();
  } catch (err) {
    showError(err);
  }
});

leaveButton.addEventListener("click", () => {
  void room?.disconnect();
});
