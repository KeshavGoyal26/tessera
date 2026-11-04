/** Base class for every error thrown by the SDK. */
export class TesseraError extends Error {
  constructor(message: string, options?: ErrorOptions) {
    super(message, options);
    this.name = new.target.name;
  }
}

/** Thrown by parts of the public API that are not implemented yet. */
export class NotImplementedError extends TesseraError {
  constructor(feature: string) {
    super(`${feature} is not implemented yet`);
  }
}

/** Why a signaling connection could not be established or was lost. */
export type ConnectionErrorCode =
  /** The server rejected the access token (HTTP 401/403). */
  | "not_allowed"
  /** The server or network could not be reached. */
  | "server_unreachable"
  /** The server does not support this SDK's protocol version (HTTP 426). */
  | "unsupported_protocol"
  /** The operation was not attempted because no connection is open. */
  | "not_connected"
  /** The connection attempt was cancelled, for example by `disconnect()`. */
  | "cancelled"
  /** The server did not answer in time. */
  | "timeout"
  /** The room does not exist (HTTP 404). */
  | "not_found"
  /** Too many requests (HTTP 429). */
  | "rate_limited"
  /** The session can no longer be resumed; a full reconnect is needed (HTTP 409). */
  | "resume_failed"
  /** Any other failure. */
  | "internal";

/** A signaling or media connection failure. */
export class ConnectionError extends TesseraError {
  /** Machine-readable failure category. */
  readonly code: ConnectionErrorCode;
  /** HTTP status of the failed WebSocket upgrade, when known. */
  readonly status: number | undefined;

  constructor(
    code: ConnectionErrorCode,
    message: string,
    options?: ErrorOptions & { status?: number },
  ) {
    super(message, options);
    this.code = code;
    this.status = options?.status;
  }
}
