export { ConnectionError, NotImplementedError, TesseraError } from "./errors.js";
export type { ConnectionErrorCode } from "./errors.js";
export { EventEmitter, RoomEvent } from "./events.js";
export type { EventMap } from "./events.js";
export { protocolVersion, version } from "./version.js";

export { ConnectionState, Room, reconnectDelay } from "./room/Room.js";
export type { RoomDeps, RoomEventMap, RoomOptions } from "./room/Room.js";
export { Participant } from "./room/Participant.js";
export { LocalParticipant } from "./room/LocalParticipant.js";
export type { DataPublishOptions, MediaDeviceKind } from "./room/LocalParticipant.js";
export { RemoteParticipant } from "./room/RemoteParticipant.js";

export { Track, TrackSource } from "./track/Track.js";
export type { Dimensions, TrackKind } from "./track/Track.js";
export { LocalTrack } from "./track/LocalTrack.js";
export type { TrackPublishOptions } from "./track/LocalTrack.js";
export { RemoteTrack } from "./track/RemoteTrack.js";
export {
  LocalTrackPublication,
  RemoteTrackPublication,
  TrackPublication,
} from "./track/TrackPublication.js";

export { VideoPresets } from "./rtc/video.js";
export type { VideoPreset } from "./rtc/video.js";
export { PCTransport } from "./rtc/PCTransport.js";
export { StatsSampler } from "./rtc/stats.js";
export type { MediaStatsSummary, StatsSummary } from "./rtc/stats.js";
export {
  PING_TIMEOUT_CODE,
  SignalClient,
  buildSignalUrl,
  decodeResponse,
  encodeRequest,
} from "./signal/SignalClient.js";
export type {
  AddTrackInit,
  CloseInfo,
  SignalConnectOptions,
  SignalRequestBody,
  TrackSettingsInit,
} from "./signal/SignalClient.js";

export {
  ConnectionQuality,
  DisconnectReason,
  VideoQuality,
} from "./gen/tessera/v1/models_pb.js";
export { SignalTarget, StreamState } from "./gen/tessera/v1/signal_pb.js";
