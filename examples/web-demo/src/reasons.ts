import { DisconnectReason } from "@tessera/client";

/** What to tell the user when the server ended their session (nothing when they left). */
export function leftMessage(reason: DisconnectReason | undefined): string {
  switch (reason) {
    case DisconnectReason.DUPLICATE_IDENTITY:
      return "You joined this room from another tab or device, so this one was disconnected.";
    case DisconnectReason.PARTICIPANT_REMOVED:
      return "You were removed from the room.";
    case DisconnectReason.ROOM_DELETED:
      return "The room was ended.";
    case DisconnectReason.SIGNAL_TIMEOUT:
      return "The connection to the server was lost.";
    case DisconnectReason.CLIENT_INITIATED:
    case undefined:
      return "";
  }
  return "You were disconnected.";
}
