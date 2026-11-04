package io.github.keshavgoyal26.tessera;

import io.github.keshavgoyal26.tessera.internal.TwirpClient;
import java.net.http.HttpClient;
import java.time.Clock;
import java.time.Duration;
import java.util.Objects;

/**
 * Connection settings shared by {@link RoomServiceClient} and {@link RecordingServiceClient}.
 *
 * <pre>{@code
 * TesseraConfig config = TesseraConfig.builder()
 *     .baseUrl("https://tessera.example.com")
 *     .apiKey("APIxxxx")
 *     .secret(System.getenv("TESSERA_API_SECRET"))
 *     .timeout(Duration.ofSeconds(5))
 *     .build();
 * RoomServiceClient rooms = RoomServiceClient.create(config);
 * }</pre>
 *
 * <p>Instances are immutable and thread-safe.
 */
public final class TesseraConfig {

  /** Default per-request timeout: 10 seconds. */
  public static final Duration DEFAULT_TIMEOUT = Duration.ofSeconds(10);

  /** Lifetime of the admin tokens minted for each call: 10 minutes. */
  public static final Duration DEFAULT_ADMIN_TOKEN_TTL = Duration.ofMinutes(10);

  private final String baseUrl;
  private final String apiKey;
  private final String secret;
  private final boolean allowShortSecret;
  private final HttpClient httpClient;
  private final Duration timeout;
  private final Duration adminTokenTtl;
  private final Clock clock;

  private TesseraConfig(Builder b) {
    this.baseUrl = TwirpClient.normalizeBaseUrl(b.baseUrl);
    this.apiKey = Objects.requireNonNull(b.apiKey, "apiKey");
    this.secret = Objects.requireNonNull(b.secret, "secret");
    this.allowShortSecret = b.allowShortSecret;
    this.httpClient = b.httpClient != null ? b.httpClient : defaultHttpClient(b.timeout);
    this.timeout = Objects.requireNonNull(b.timeout, "timeout");
    this.adminTokenTtl = Objects.requireNonNull(b.adminTokenTtl, "adminTokenTtl");
    this.clock = Objects.requireNonNull(b.clock, "clock");
    // Fail fast on an invalid key/secret instead of on the first call.
    adminToken(RoomGrant.builder().build());
  }

  private static HttpClient defaultHttpClient(Duration timeout) {
    return HttpClient.newBuilder()
        .connectTimeout(timeout == null ? DEFAULT_TIMEOUT : timeout)
        .followRedirects(HttpClient.Redirect.NEVER)
        .build();
  }

  /**
   * Returns a new builder.
   *
   * @return a builder
   */
  public static Builder builder() {
    return new Builder();
  }

  /**
   * Returns the normalized server base URL.
   *
   * @return the base URL
   */
  public String getBaseUrl() {
    return baseUrl;
  }

  /**
   * Returns the API key.
   *
   * @return the API key
   */
  public String getApiKey() {
    return apiKey;
  }

  /**
   * Returns the HTTP client.
   *
   * @return the HTTP client
   */
  public HttpClient getHttpClient() {
    return httpClient;
  }

  /**
   * Returns the per-request timeout.
   *
   * @return the timeout
   */
  public Duration getTimeout() {
    return timeout;
  }

  /**
   * Returns the lifetime of per-call admin tokens.
   *
   * @return the TTL
   */
  public Duration getAdminTokenTtl() {
    return adminTokenTtl;
  }

  /** Mints a short-lived admin token with the given grants. */
  String adminToken(RoomGrant grants) {
    return AccessToken.builder()
        .apiKey(apiKey)
        .secret(secret)
        .allowShortSecret(allowShortSecret)
        .ttl(adminTokenTtl)
        .clock(clock)
        .grants(grants)
        .toJwt();
  }

  /** Creates the Twirp client for these settings. */
  TwirpClient twirpClient() {
    return new TwirpClient(baseUrl, httpClient, timeout);
  }

  @Override
  public String toString() {
    return "TesseraConfig{baseUrl=" + baseUrl + ", apiKey=" + apiKey + ", timeout=" + timeout + "}";
  }

  /** Mutable builder for {@link TesseraConfig}. Not thread-safe. */
  public static final class Builder {
    private String baseUrl;
    private String apiKey;
    private String secret;
    private boolean allowShortSecret;
    private HttpClient httpClient;
    private Duration timeout = DEFAULT_TIMEOUT;
    private Duration adminTokenTtl = DEFAULT_ADMIN_TOKEN_TTL;
    private Clock clock = Clock.systemUTC();

    private Builder() {}

    /**
     * Sets the server URL, e.g. {@code https://tessera.example.com}. Required.
     *
     * @param baseUrl the URL ({@code http(s)://} or {@code ws(s)://})
     * @return this builder
     */
    public Builder baseUrl(String baseUrl) {
      this.baseUrl = baseUrl;
      return this;
    }

    /**
     * Sets the API key. Required.
     *
     * @param apiKey the API key
     * @return this builder
     */
    public Builder apiKey(String apiKey) {
      this.apiKey = apiKey;
      return this;
    }

    /**
     * Sets the API secret. Required; at least 32 bytes.
     *
     * @param secret the secret
     * @return this builder
     */
    public Builder secret(String secret) {
      this.secret = secret;
      return this;
    }

    /**
     * Allows secrets shorter than 32 bytes (development servers only).
     *
     * @param allow whether to allow short secrets
     * @return this builder
     */
    public Builder allowShortSecret(boolean allow) {
      this.allowShortSecret = allow;
      return this;
    }

    /**
     * Sets the HTTP client. Defaults to a new client without redirects.
     *
     * @param httpClient the client
     * @return this builder
     */
    public Builder httpClient(HttpClient httpClient) {
      this.httpClient = httpClient;
      return this;
    }

    /**
     * Sets the per-request timeout (default 10 s).
     *
     * @param timeout the timeout
     * @return this builder
     */
    public Builder timeout(Duration timeout) {
      this.timeout = timeout;
      return this;
    }

    /**
     * Sets the lifetime of the admin token minted for each call (default 10 min).
     *
     * @param ttl the lifetime
     * @return this builder
     */
    public Builder adminTokenTtl(Duration ttl) {
      this.adminTokenTtl = ttl;
      return this;
    }

    /**
     * Sets the clock used for token timestamps.
     *
     * @param clock the clock
     * @return this builder
     */
    public Builder clock(Clock clock) {
      this.clock = clock;
      return this;
    }

    /**
     * Validates and builds the configuration.
     *
     * @return the configuration
     * @throws IllegalArgumentException if the URL, key or secret is invalid
     * @throws NullPointerException if a required value is missing
     */
    public TesseraConfig build() {
      return new TesseraConfig(this);
    }
  }
}
