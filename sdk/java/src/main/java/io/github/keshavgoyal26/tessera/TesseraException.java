package io.github.keshavgoyal26.tessera;

import java.util.Map;

/**
 * Error raised by the Tessera SDK.
 *
 * <p>For admin API calls the {@linkplain #getCode() code} is the Twirp error code returned by the
 * server (for example {@code not_found}, {@code invalid_argument}, {@code permission_denied},
 * {@code unauthenticated}, {@code already_exists}, {@code resource_exhausted}, {@code
 * unimplemented}, {@code unavailable} or {@code internal}) and {@linkplain #getHttpStatus() the
 * HTTP status} is the status of the response. Transport failures use the code {@code unavailable}
 * (or {@code deadline_exceeded} for timeouts) with HTTP status {@code 0}.
 */
public class TesseraException extends RuntimeException {

  private static final long serialVersionUID = 1L;

  /** Twirp code for a resource that does not exist. */
  public static final String NOT_FOUND = "not_found";
  /** Twirp code for invalid request arguments. */
  public static final String INVALID_ARGUMENT = "invalid_argument";
  /** Twirp code for a missing or invalid token. */
  public static final String UNAUTHENTICATED = "unauthenticated";
  /** Twirp code for a token without the required grant. */
  public static final String PERMISSION_DENIED = "permission_denied";
  /** Twirp code for an unexpected server error. */
  public static final String INTERNAL = "internal";
  /** Twirp code for an unreachable or overloaded server. */
  public static final String UNAVAILABLE = "unavailable";
  /** Twirp code for a request that is malformed and cannot be decoded. */
  public static final String MALFORMED = "malformed";

  private final String code;
  private final int httpStatus;
  private final transient Map<String, String> meta;

  /**
   * Creates an exception.
   *
   * @param code Twirp error code
   * @param message human-readable message
   * @param httpStatus HTTP status of the response, or 0 when there was none
   */
  public TesseraException(String code, String message, int httpStatus) {
    this(code, message, httpStatus, Map.of(), null);
  }

  /**
   * Creates an exception with Twirp metadata and a cause.
   *
   * @param code Twirp error code
   * @param message human-readable message
   * @param httpStatus HTTP status of the response, or 0 when there was none
   * @param meta Twirp error metadata (may be empty, never {@code null})
   * @param cause underlying cause, or {@code null}
   */
  public TesseraException(
      String code, String message, int httpStatus, Map<String, String> meta, Throwable cause) {
    super(message, cause);
    this.code = code;
    this.httpStatus = httpStatus;
    this.meta = meta == null ? Map.of() : Map.copyOf(meta);
  }

  /**
   * Returns the Twirp error code, for example {@code not_found}.
   *
   * @return the error code
   */
  public String getCode() {
    return code;
  }

  /**
   * Returns the HTTP status of the failed response, or 0 when no response was received.
   *
   * @return the HTTP status
   */
  public int getHttpStatus() {
    return httpStatus;
  }

  /**
   * Returns the Twirp error metadata ({@code meta} object of the error body).
   *
   * @return an immutable map, possibly empty
   */
  public Map<String, String> getMeta() {
    return meta == null ? Map.of() : meta;
  }

  @Override
  public String toString() {
    return getClass().getName() + ": " + code + " (HTTP " + httpStatus + "): " + getMessage();
  }
}
