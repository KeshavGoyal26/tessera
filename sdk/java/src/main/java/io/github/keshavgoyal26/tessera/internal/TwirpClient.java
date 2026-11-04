package io.github.keshavgoyal26.tessera.internal;

import com.google.protobuf.InvalidProtocolBufferException;
import com.google.protobuf.Message;
import com.google.protobuf.Parser;
import io.github.keshavgoyal26.tessera.TesseraException;
import java.io.IOException;
import java.net.URI;
import java.net.http.HttpClient;
import java.net.http.HttpRequest;
import java.net.http.HttpResponse;
import java.net.http.HttpTimeoutException;
import java.nio.charset.StandardCharsets;
import java.time.Duration;
import java.util.LinkedHashMap;
import java.util.Map;
import java.util.Objects;

/**
 * Minimal Twirp v7 client speaking the binary protobuf flavour (ADR-0008).
 *
 * <p>This class is internal to the SDK and is not part of its public API.
 *
 * <p>Requests are sent as {@code POST {baseUrl}/twirp/tessera.v1.{Service}/{Method}} with {@code
 * Content-Type: application/protobuf}. Non-2xx responses are decoded as Twirp error JSON and
 * thrown as {@link TesseraException}.
 */
public final class TwirpClient {

  /** Protobuf package of all Tessera services. */
  public static final String PACKAGE = "tessera.v1";

  private static final String PROTOBUF = "application/protobuf";

  private final String baseUrl;
  private final HttpClient httpClient;
  private final Duration timeout;

  /**
   * Creates a client.
   *
   * @param baseUrl server URL, e.g. {@code https://tessera.example.com}; {@code ws://} and {@code
   *     wss://} are mapped to {@code http://} and {@code https://}
   * @param httpClient HTTP client to use
   * @param timeout per-request timeout
   */
  public TwirpClient(String baseUrl, HttpClient httpClient, Duration timeout) {
    this.baseUrl = normalizeBaseUrl(baseUrl);
    this.httpClient = Objects.requireNonNull(httpClient, "httpClient");
    this.timeout = Objects.requireNonNull(timeout, "timeout");
  }

  /**
   * Normalizes a base URL: maps WebSocket schemes to HTTP and strips trailing slashes.
   *
   * @param baseUrl the URL
   * @return the normalized URL
   */
  public static String normalizeBaseUrl(String baseUrl) {
    Objects.requireNonNull(baseUrl, "baseUrl");
    String url = baseUrl.trim();
    if (url.startsWith("ws://")) {
      url = "http://" + url.substring(5);
    } else if (url.startsWith("wss://")) {
      url = "https://" + url.substring(6);
    }
    if (!url.startsWith("http://") && !url.startsWith("https://")) {
      throw new IllegalArgumentException("baseUrl must start with http(s):// or ws(s)://");
    }
    while (url.endsWith("/")) {
      url = url.substring(0, url.length() - 1);
    }
    return url;
  }

  /**
   * Returns the normalized base URL.
   *
   * @return the base URL without a trailing slash
   */
  public String baseUrl() {
    return baseUrl;
  }

  /**
   * Calls a Twirp method.
   *
   * @param service service name without package, e.g. {@code RoomService}
   * @param method method name, e.g. {@code CreateRoom}
   * @param request request message
   * @param parser parser of the response message
   * @param bearerToken access token sent as {@code Authorization: Bearer}; may be {@code null}
   * @param <T> response type
   * @return the decoded response
   * @throws TesseraException on Twirp errors, transport failures or undecodable responses
   */
  public <T extends Message> T call(
      String service, String method, Message request, Parser<T> parser, String bearerToken) {
    URI uri = URI.create(baseUrl + "/twirp/" + PACKAGE + "." + service + "/" + method);
    HttpRequest.Builder rb =
        HttpRequest.newBuilder(uri)
            .timeout(timeout)
            .header("Content-Type", PROTOBUF)
            .header("Accept", PROTOBUF)
            .POST(HttpRequest.BodyPublishers.ofByteArray(request.toByteArray()));
    if (bearerToken != null && !bearerToken.isEmpty()) {
      rb.header("Authorization", "Bearer " + bearerToken);
    }

    HttpResponse<byte[]> resp;
    try {
      resp = httpClient.send(rb.build(), HttpResponse.BodyHandlers.ofByteArray());
    } catch (HttpTimeoutException e) {
      throw new TesseraException(
          "deadline_exceeded", service + "/" + method + " timed out", 0, Map.of(), e);
    } catch (IOException e) {
      throw new TesseraException(
          TesseraException.UNAVAILABLE,
          service + "/" + method + " failed: " + e.getMessage(),
          0,
          Map.of(),
          e);
    } catch (InterruptedException e) {
      Thread.currentThread().interrupt();
      throw new TesseraException(
          "canceled", service + "/" + method + " was interrupted", 0, Map.of(), e);
    }

    int status = resp.statusCode();
    byte[] body = resp.body() == null ? new byte[0] : resp.body();
    if (status < 200 || status >= 300) {
      throw errorFromResponse(status, body);
    }
    try {
      return parser.parseFrom(body);
    } catch (InvalidProtocolBufferException e) {
      throw new TesseraException(
          TesseraException.INTERNAL,
          "invalid protobuf response from " + service + "/" + method,
          status,
          Map.of(),
          e);
    }
  }

  /**
   * Decodes a Twirp error response. Bodies that are not Twirp error JSON (for example from a proxy)
   * are mapped from the HTTP status, as Twirp clients do.
   *
   * @param status HTTP status
   * @param body response body
   * @return the exception to throw
   */
  public static TesseraException errorFromResponse(int status, byte[] body) {
    String text = new String(body, StandardCharsets.UTF_8);
    try {
      if (Json.parse(text) instanceof Map<?, ?> obj && obj.get("code") instanceof String code) {
        String msg = obj.get("msg") instanceof String m ? m : "";
        Map<String, String> meta = new LinkedHashMap<>();
        if (obj.get("meta") instanceof Map<?, ?> rawMeta) {
          rawMeta.forEach(
              (k, v) -> {
                if (v != null) {
                  meta.put(String.valueOf(k), String.valueOf(v));
                }
              });
        }
        return new TesseraException(code, msg, status, meta, null);
      }
    } catch (IllegalArgumentException notJson) {
      // fall through
    }
    Map<String, String> meta = new LinkedHashMap<>();
    meta.put("http_error_from_intermediary", "true");
    if (!text.isEmpty()) {
      meta.put("body", text.length() > 512 ? text.substring(0, 512) : text);
    }
    return new TesseraException(
        codeForStatus(status), "unexpected HTTP status " + status, status, meta, null);
  }

  private static String codeForStatus(int status) {
    return switch (status) {
      case 400 -> TesseraException.INTERNAL;
      case 401 -> TesseraException.UNAUTHENTICATED;
      case 403 -> TesseraException.PERMISSION_DENIED;
      case 404 -> "bad_route";
      case 429, 502, 503, 504 -> TesseraException.UNAVAILABLE;
      default -> status >= 300 && status < 400 ? TesseraException.INTERNAL : "unknown";
    };
  }
}
