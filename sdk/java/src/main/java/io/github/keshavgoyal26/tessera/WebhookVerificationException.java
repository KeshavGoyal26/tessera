package io.github.keshavgoyal26.tessera;

/**
 * Thrown by {@link WebhookReceiver} when a webhook request cannot be authenticated: missing or
 * malformed headers, an unknown API key, a timestamp outside the tolerance window or a signature
 * mismatch. Applications should answer such requests with HTTP 401 and must not process the body.
 */
public class WebhookVerificationException extends TesseraException {

  private static final long serialVersionUID = 1L;

  /**
   * Creates an exception with code {@code unauthenticated} and HTTP status 401.
   *
   * @param message what failed
   */
  public WebhookVerificationException(String message) {
    super(UNAUTHENTICATED, message, 401);
  }
}
