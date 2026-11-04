package io.github.keshavgoyal26.tessera;

import com.google.protobuf.InvalidProtocolBufferException;
import io.github.keshavgoyal26.tessera.proto.v1.WebhookEvent;
import java.nio.charset.StandardCharsets;
import java.security.MessageDigest;
import java.time.Clock;
import java.time.Duration;
import java.util.ArrayList;
import java.util.HexFormat;
import java.util.List;
import java.util.Locale;
import java.util.Map;
import java.util.Objects;
import java.util.TreeMap;

/**
 * Verifies and decodes webhook requests sent by the Tessera server (ADR-0008).
 *
 * <p>Every request carries these headers:
 *
 * <ul>
 *   <li>{@code Tessera-Key}: the API key whose secret signed the request;
 *   <li>{@code Tessera-Signature: t=<unix seconds>,v1=<hex>} where {@code hex} is {@code
 *       HMAC-SHA256(secret, t + "." + rawBody)};
 *   <li>{@code Tessera-Event-Id} and {@code Tessera-Event-Type}.
 * </ul>
 *
 * <p>{@link #receive(byte[], Map)} checks the signature in constant time, rejects timestamps more
 * than {@linkplain #DEFAULT_TOLERANCE 300 seconds} away from now and decodes the body. Bodies with
 * {@code Content-Type: application/protobuf} are decoded as binary protobuf; anything else is
 * treated as protojson, which requires the optional {@code com.google.protobuf:protobuf-java-util}
 * dependency on the classpath.
 *
 * <pre>{@code
 * WebhookReceiver receiver = new WebhookReceiver("APIxxxx", System.getenv("TESSERA_API_SECRET"));
 * WebhookEvent event = receiver.receive(rawBody, headers); // throws WebhookVerificationException
 * }</pre>
 *
 * <p>Always pass the raw request body bytes, exactly as received; re-serialized JSON will not
 * verify. Delivery is at-least-once, so deduplicate on {@link WebhookEvent#getId()}.
 *
 * <p>Instances are immutable and thread-safe.
 */
public final class WebhookReceiver {

  /** Default allowed clock skew between the signature timestamp and now: 300 seconds. */
  public static final Duration DEFAULT_TOLERANCE = Duration.ofSeconds(300);

  /** Header naming the API key that signed the request. */
  public static final String HEADER_KEY = "Tessera-Key";
  /** Header carrying the signature. */
  public static final String HEADER_SIGNATURE = "Tessera-Signature";
  /** Header carrying the event id (idempotency key). */
  public static final String HEADER_EVENT_ID = "Tessera-Event-Id";
  /** Header carrying the event type. */
  public static final String HEADER_EVENT_TYPE = "Tessera-Event-Type";

  private static final HexFormat HEX = HexFormat.of();

  private final Map<String, byte[]> secrets;
  private final Duration tolerance;
  private final Clock clock;

  /**
   * Creates a receiver for a single API key.
   *
   * @param apiKey the API key expected in {@code Tessera-Key}
   * @param secret its secret
   */
  public WebhookReceiver(String apiKey, String secret) {
    this(Map.of(Objects.requireNonNull(apiKey, "apiKey"), Objects.requireNonNull(secret, "secret")));
  }

  /**
   * Creates a receiver accepting several API keys (for example during key rotation).
   *
   * @param apiKeyToSecret map of API key to secret
   */
  public WebhookReceiver(Map<String, String> apiKeyToSecret) {
    this(toSecretBytes(apiKeyToSecret), DEFAULT_TOLERANCE, Clock.systemUTC());
  }

  private WebhookReceiver(Map<String, byte[]> secrets, Duration tolerance, Clock clock) {
    this.secrets = secrets;
    this.tolerance = tolerance;
    this.clock = clock;
  }

  private static Map<String, byte[]> toSecretBytes(Map<String, String> apiKeyToSecret) {
    Objects.requireNonNull(apiKeyToSecret, "apiKeyToSecret");
    if (apiKeyToSecret.isEmpty()) {
      throw new IllegalArgumentException("at least one API key is required");
    }
    Map<String, byte[]> m = new TreeMap<>();
    apiKeyToSecret.forEach(
        (k, v) -> {
          if (k == null || k.isEmpty() || v == null || v.isEmpty()) {
            throw new IllegalArgumentException("API keys and secrets must be non-empty");
          }
          m.put(k, v.getBytes(StandardCharsets.UTF_8));
        });
    return Map.copyOf(m);
  }

  /**
   * Returns a copy of this receiver with a different timestamp tolerance.
   *
   * @param tolerance maximum allowed {@code |now - t|}
   * @return a new receiver
   */
  public WebhookReceiver withTolerance(Duration tolerance) {
    Objects.requireNonNull(tolerance, "tolerance");
    if (tolerance.isNegative()) {
      throw new IllegalArgumentException("tolerance must not be negative");
    }
    return new WebhookReceiver(secrets, tolerance, clock);
  }

  /**
   * Returns a copy of this receiver using a different clock (useful for tests).
   *
   * @param clock the clock
   * @return a new receiver
   */
  public WebhookReceiver withClock(Clock clock) {
    return new WebhookReceiver(secrets, tolerance, Objects.requireNonNull(clock, "clock"));
  }

  /**
   * Verifies the request signature and decodes the event.
   *
   * @param body the raw request body
   * @param headers request headers; names are matched case-insensitively
   * @return the verified event
   * @throws WebhookVerificationException if the request is not authentic
   * @throws TesseraException with code {@code malformed} if the body cannot be decoded, or {@code
   *     unimplemented} if the body is JSON and {@code protobuf-java-util} is not on the classpath
   */
  public WebhookEvent receive(byte[] body, Map<String, String> headers) {
    Objects.requireNonNull(body, "body");
    Map<String, String> h = new TreeMap<>(String.CASE_INSENSITIVE_ORDER);
    if (headers != null) {
      headers.forEach(
          (k, v) -> {
            if (k != null && v != null) {
              h.put(k, v);
            }
          });
    }
    verify(body, h);
    return decode(body, h.get("Content-Type"));
  }

  private void verify(byte[] body, Map<String, String> headers) {
    String apiKey = headers.get(HEADER_KEY);
    if (apiKey == null || apiKey.isBlank()) {
      throw new WebhookVerificationException("missing " + HEADER_KEY + " header");
    }
    byte[] secret = secrets.get(apiKey.trim());
    if (secret == null) {
      throw new WebhookVerificationException("unknown API key in " + HEADER_KEY + " header");
    }
    String sigHeader = headers.get(HEADER_SIGNATURE);
    if (sigHeader == null || sigHeader.isBlank()) {
      throw new WebhookVerificationException("missing " + HEADER_SIGNATURE + " header");
    }

    String timestamp = null;
    List<byte[]> candidates = new ArrayList<>();
    for (String part : sigHeader.split(",")) {
      int eq = part.indexOf('=');
      if (eq <= 0) {
        continue;
      }
      String k = part.substring(0, eq).trim();
      String v = part.substring(eq + 1).trim();
      if (k.equals("t")) {
        timestamp = v;
      } else if (k.equals("v1")) {
        try {
          candidates.add(HEX.parseHex(v.toLowerCase(Locale.ROOT)));
        } catch (IllegalArgumentException ignored) {
          // A malformed candidate simply cannot match.
        }
      }
    }
    if (timestamp == null || candidates.isEmpty()) {
      throw new WebhookVerificationException("malformed " + HEADER_SIGNATURE + " header");
    }
    long t;
    try {
      t = Long.parseLong(timestamp);
    } catch (NumberFormatException e) {
      throw new WebhookVerificationException("malformed timestamp in " + HEADER_SIGNATURE);
    }
    long now = clock.instant().getEpochSecond();
    if (Math.abs(now - t) > tolerance.getSeconds()) {
      throw new WebhookVerificationException("signature timestamp outside the tolerance window");
    }

    byte[] expected = computeSignature(secret, timestamp, body);
    boolean ok = false;
    for (byte[] candidate : candidates) {
      // Evaluate every candidate to keep timing independent of position.
      ok |= MessageDigest.isEqual(expected, candidate);
    }
    if (!ok) {
      throw new WebhookVerificationException("signature mismatch");
    }
  }

  private static WebhookEvent decode(byte[] body, String contentType) {
    String ct = contentType == null ? "" : contentType.toLowerCase(Locale.ROOT).trim();
    if (ct.startsWith("application/protobuf")
        || ct.startsWith("application/x-protobuf")
        || ct.startsWith("application/vnd.google.protobuf")) {
      try {
        return WebhookEvent.parseFrom(body);
      } catch (InvalidProtocolBufferException e) {
        throw new TesseraException(
            TesseraException.MALFORMED, "invalid protobuf webhook body", 0, Map.of(), e);
      }
    }
    if (!JsonSupport.AVAILABLE) {
      throw new TesseraException(
          "unimplemented",
          "received a JSON webhook but com.google.protobuf:protobuf-java-util is not on the"
              + " classpath: add that dependency, or configure the webhook endpoint with"
              + " protobuf encoding",
          0);
    }
    return JsonSupport.parse(body);
  }

  /**
   * Computes the {@code Tessera-Signature} header value for a body, as the server does.
   *
   * <p>Useful for tests and for local tools that replay webhooks.
   *
   * @param secret the API secret
   * @param timestamp Unix seconds
   * @param body the raw body
   * @return {@code t=<timestamp>,v1=<hex signature>}
   */
  public static String sign(String secret, long timestamp, byte[] body) {
    Objects.requireNonNull(secret, "secret");
    Objects.requireNonNull(body, "body");
    String t = Long.toString(timestamp);
    byte[] sig = computeSignature(secret.getBytes(StandardCharsets.UTF_8), t, body);
    return "t=" + t + ",v1=" + HEX.formatHex(sig);
  }

  private static byte[] computeSignature(byte[] secret, String timestamp, byte[] body) {
    byte[] prefix = (timestamp + ".").getBytes(StandardCharsets.US_ASCII);
    byte[] data = new byte[prefix.length + body.length];
    System.arraycopy(prefix, 0, data, 0, prefix.length);
    System.arraycopy(body, 0, data, prefix.length, body.length);
    return AccessToken.hmacSha256(secret, data);
  }

  /** Detects protobuf-java-util without linking to it. */
  private static final class JsonSupport {
    static final boolean AVAILABLE = detect();

    private static boolean detect() {
      try {
        Class.forName(
            "com.google.protobuf.util.JsonFormat", false, WebhookReceiver.class.getClassLoader());
        return true;
      } catch (ClassNotFoundException | LinkageError e) {
        return false;
      }
    }

    static WebhookEvent parse(byte[] body) {
      return JsonDecoder.parse(body);
    }
  }

  /** Only loaded when {@link JsonSupport#AVAILABLE} is true, so JsonFormat is never linked early. */
  private static final class JsonDecoder {
    static WebhookEvent parse(byte[] body) {
      WebhookEvent.Builder b = WebhookEvent.newBuilder();
      try {
        com.google.protobuf.util.JsonFormat.parser()
            .ignoringUnknownFields()
            .merge(new String(body, StandardCharsets.UTF_8), b);
      } catch (InvalidProtocolBufferException e) {
        throw new TesseraException(
            TesseraException.MALFORMED, "invalid JSON webhook body", 0, Map.of(), e);
      }
      return b.build();
    }
  }
}
