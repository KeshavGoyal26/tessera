export { ConnectionError, NotImplementedError, TesseraError } from "./errors.js";
export type { ConnectionErrorCode } from "./errors.js";
export { EventEmitter, RoomEvent } from "./events.js";
export type { EventMap } from "./events.js";
export { protocolVersion, version } from "./version.js";

export { ConnectionState, Room } from "./room/Room.js";
export type { RoomEventMap, RoomOptions } from "./room/Room.js";
export { Participant } from "./room/Participant.js";
export { LocalParticipant } from "./room/LocalParticipant.js";
export type { DataPublishOptions } from "./room/LocalParticipant.js";
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

export { MIN_SIMULCAST_LAYER_SIZE, VideoPresets, computeSimulcastEncodings } from "./rtc/simulcast.js";
export type { VideoPreset } from "./rtc/simulcast.js";
export { PCTransport } from "./rtc/PCTransport.js";
export {
  SignalClient,
  buildSignalUrl,
  decodeResponse,
  encodeRequest,
} from "./signal/SignalClient.js";
export type {
  AddTrackInit,
  SignalConnectOptions,
  SignalRequestBody,
  TrackSettingsInit,
} from "./signal/SignalClient.js";

export {
  ConnectionQuality,
  DisconnectReason,
  VideoQuality,
} from "./gen/tessera/v1/models_pb.js";
export { SignalTarget } from "./gen/tessera/v1/signal_pb.js";
