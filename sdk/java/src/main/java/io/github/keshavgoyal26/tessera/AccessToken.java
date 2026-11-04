package io.github.keshavgoyal26.tessera;

import io.github.keshavgoyal26.tessera.internal.Json;
import java.nio.charset.StandardCharsets;
import java.security.GeneralSecurityException;
import java.time.Clock;
import java.time.Duration;
import java.util.Base64;
import java.util.LinkedHashMap;
import java.util.Map;
import java.util.Objects;
import java.util.TreeMap;
import javax.crypto.Mac;
import javax.crypto.spec.SecretKeySpec;

/**
 * An HS256 JWT access token for Tessera (ADR-0004).
 *
 * <p>Tokens authorize end users to join rooms and authorize backends to call the admin API. They
 * are signed with the secret of the API key placed in {@code iss}.
 *
 * <pre>{@code
 * String jwt = AccessToken.builder()
 *     .apiKey("APIxxxx")
 *     .secret(System.getenv("TESSERA_API_SECRET"))
 *     .identity("alice")
 *     .name("Alice")
 *     .ttl(Duration.ofHours(1))
 *     .grants(RoomGrant.builder().room("standup").roomJoin(true).build())
 *     .toJwt();
 * }</pre>
 *
 * <p>Instances are immutable and thread-safe; {@link #toJwt()} computes {@code nbf} and {@code
 * exp} from the configured clock each time it is called.
 */
public final class AccessToken {

  /** Default token lifetime: 6 hours. */
  public static final Duration DEFAULT_TTL = Duration.ofHours(6);

  /** Maximum lifetime the server accepts for tokens with {@code room_join}: 24 hours. */
  public static final Duration MAX_JOIN_TTL = Duration.ofHours(24);

  /** Minimum secret length in bytes accepted without {@link Builder#allowShortSecret(boolean)}. */
  public static final int MIN_SECRET_BYTES = 32;

  private static final String HEADER_JSON = "{\"alg\":\"HS256\",\"typ\":\"JWT\"}";
  private static final Base64.Encoder B64URL = Base64.getUrlEncoder().withoutPadding();

  private final String apiKey;
  private final byte[] secret;
  private final String identity;
  private final String name;
  private final String metadata;
  private final Map<String, String> attributes;
  private final Duration ttl;
  private final RoomGrant grants;
  private final String jwtId;
  private final Clock clock;

  private AccessToken(Builder b) {
    if (b.apiKey == null || b.apiKey.isEmpty()) {
      throw new IllegalArgumentException("apiKey is required");
    }
    if (b.secret == null || b.secret.length == 0) {
      throw new IllegalArgumentException("secret is required");
    }
    if (b.secret.length < MIN_SECRET_BYTES && !b.allowShortSecret) {
      throw new IllegalArgumentException(
          "secret must be at least "
              + MIN_SECRET_BYTES
              + " bytes (use allowShortSecret(true) for development servers only)");
    }
    if (b.ttl == null || b.ttl.isNegative() || b.ttl.isZero()) {
      throw new IllegalArgumentException("ttl must be positive");
    }
    RoomGrant g = b.grants == null ? RoomGrant.builder().build() : b.grants;
    if (g.isRoomJoin()) {
      if (b.identity == null || b.identity.isEmpty()) {
        throw new IllegalArgumentException("identity is required for room_join tokens");
      }
      if (g.getRoom().isEmpty()) {
        throw new IllegalArgumentException("grants.room is required for room_join tokens");
      }
      if (b.ttl.compareTo(MAX_JOIN_TTL) > 0) {
        throw new IllegalArgumentException("ttl of room_join tokens must not exceed 24h");
      }
    }
    this.apiKey = b.apiKey;
    this.secret = b.secret.clone();
    this.identity = b.identity == null ? "" : b.identity;
    this.name = b.name == null ? "" : b.name;
    this.metadata = b.metadata == null ? "" : b.metadata;
    this.attributes = Map.copyOf(b.attributes);
    this.ttl = b.ttl;
    this.grants = g;
    this.jwtId = b.jwtId == null ? "" : b.jwtId;
    this.clock = b.clock;
  }

  /**
   * Returns a new builder.
   *
   * @return a builder with {@link #DEFAULT_TTL} and the system UTC clock
   */
  public static Builder builder() {
    return new Builder();
  }

  /**
   * Returns the API key ({@code iss}).
   *
   * @return the API key
   */
  public String getApiKey() {
    return apiKey;
  }

  /**
   * Returns the participant identity ({@code sub}).
   *
   * @return the identity, empty when unset
   */
  public String getIdentity() {
    return identity;
  }

  /**
   * Returns the display name ({@code name}).
   *
   * @return the name, empty when unset
   */
  public String getName() {
    return name;
  }

  /**
   * Returns the application metadata ({@code metadata}).
   *
   * @return the metadata, empty when unset
   */
  public String getMetadata() {
    return metadata;
  }

  /**
   * Returns the participant attributes ({@code attributes}).
   *
   * @return an immutable map
   */
  public Map<String, String> getAttributes() {
    return attributes;
  }

  /**
   * Returns the token lifetime.
   *
   * @return the TTL
   */
  public Duration getTtl() {
    return ttl;
  }

  /**
   * Returns the grants ({@code grants}).
   *
   * @return the grants
   */
  public RoomGrant getGrants() {
    return grants;
  }

  /**
   * Returns the claims that {@link #toJwt()} would sign now, in claim order.
   *
   * @return a new ordered map of claims
   */
  Map<String, Object> claims() {
    long now = clock.instant().getEpochSecond();
    Map<String, Object> c = new LinkedHashMap<>();
    c.put("iss", apiKey);
    if (!identity.isEmpty()) {
      c.put("sub", identity);
    }
    c.put("nbf", now);
    c.put("exp", now + ttl.getSeconds());
    if (!jwtId.isEmpty()) {
      c.put("jti", jwtId);
    }
    if (!name.isEmpty()) {
      c.put("name", name);
    }
    if (!metadata.isEmpty()) {
      c.put("metadata", metadata);
    }
    if (!attributes.isEmpty()) {
      c.put("attributes", new TreeMap<>(attributes));
    }
    c.put("grants", grants.toClaims());
    return c;
  }

  /**
   * Signs the token.
   *
   * @return the compact JWT ({@code header.payload.signature}, base64url without padding)
   */
  public String toJwt() {
    String header = B64URL.encodeToString(HEADER_JSON.getBytes(StandardCharsets.UTF_8));
    String payload = B64URL.encodeToString(Json.write(claims()).getBytes(StandardCharsets.UTF_8));
    String signingInput = header + "." + payload;
    byte[] sig = hmacSha256(secret, signingInput.getBytes(StandardCharsets.US_ASCII));
    return signingInput + "." + B64URL.encodeToString(sig);
  }

  @Override
  public String toString() {
    // Never include the secret.
    return "AccessToken{apiKey=" + apiKey + ", identity=" + identity + ", grants=" + grants + "}";
  }

  static byte[] hmacSha256(byte[] key, byte[] data) {
    try {
      Mac mac = Mac.getInstance("HmacSHA256");
      mac.init(new SecretKeySpec(key, "HmacSHA256"));
      return mac.doFinal(data);
    } catch (GeneralSecurityException e) {
      throw new IllegalStateException("HmacSHA256 is not available", e);
    }
  }

  /** Mutable builder for {@link AccessToken}. Not thread-safe. */
  public static final class Builder {
    private String apiKey;
    private byte[] secret;
    private boolean allowShortSecret;
    private String identity;
    private String name;
    private String metadata;
    private final Map<String, String> attributes = new LinkedHashMap<>();
    private Duration ttl = DEFAULT_TTL;
    private RoomGrant grants;
    private String jwtId;
    private Clock clock = Clock.systemUTC();

    private Builder() {}

    /**
     * Sets the API key ({@code iss}). Required.
     *
     * @param apiKey the API key
     * @return this builder
     */
    public Builder apiKey(String apiKey) {
      this.apiKey = apiKey;
      return this;
    }

    /**
     * Sets the API secret used to sign the token (UTF-8 bytes). Required; at least 32 bytes.
     *
     * @param secret the secret
     * @return this builder
     */
    public Builder secret(String secret) {
      this.secret = secret == null ? null : secret.getBytes(StandardCharsets.UTF_8);
      return this;
    }

    /**
     * Allows secrets shorter than 32 bytes. Only for servers running with {@code development:
     * true}.
     *
     * @param allow whether to allow short secrets
     * @return this builder
     */
    public Builder allowShortSecret(boolean allow) {
      this.allowShortSecret = allow;
      return this;
    }

    /**
     * Sets the participant identity ({@code sub}); required for {@code room_join}.
     *
     * @param identity the identity
     * @return this builder
     */
    public Builder identity(String identity) {
      this.identity = identity;
      return this;
    }

    /**
     * Sets the display name ({@code name}).
     *
     * @param name the name
     * @return this builder
     */
    public Builder name(String name) {
      this.name = name;
      return this;
    }

    /**
     * Sets opaque application metadata ({@code metadata}).
     *
     * @param metadata the metadata
     * @return this builder
     */
    public Builder metadata(String metadata) {
      this.metadata = metadata;
      return this;
    }

    /**
     * Adds one attribute ({@code attributes}).
     *
     * @param key attribute key
     * @param value attribute value
     * @return this builder
     */
    public Builder attribute(String key, String value) {
      this.attributes.put(Objects.requireNonNull(key, "key"), Objects.requireNonNull(value, "value"));
      return this;
    }

    /**
     * Replaces all attributes ({@code attributes}).
     *
     * @param attributes the attributes; {@code null} clears them
     * @return this builder
     */
    public Builder attributes(Map<String, String> attributes) {
      this.attributes.clear();
      if (attributes != null) {
        attributes.forEach(this::attribute);
      }
      return this;
    }

    /**
     * Sets the token lifetime (default 6 hours; at most 24 hours for {@code room_join}).
     *
     * @param ttl the lifetime
     * @return this builder
     */
    public Builder ttl(Duration ttl) {
      this.ttl = ttl;
      return this;
    }

    /**
     * Sets the grants ({@code grants}).
     *
     * @param grants the grants
     * @return this builder
     */
    public Builder grants(RoomGrant grants) {
      this.grants = grants;
      return this;
    }

    /**
     * Sets an optional unique token id ({@code jti}).
     *
     * @param jwtId the id
     * @return this builder
     */
    public Builder jwtId(String jwtId) {
      this.jwtId = jwtId;
      return this;
    }

    /**
     * Sets the clock used for {@code nbf} and {@code exp} (useful for tests).
     *
     * @param clock the clock
     * @return this builder
     */
    public Builder clock(Clock clock) {
      this.clock = Objects.requireNonNull(clock, "clock");
      return this;
    }

    /**
     * Validates the settings and builds the token.
     *
     * @return the token
     * @throws IllegalArgumentException if a required field is missing, the secret is too short or
     *     the TTL is invalid
     */
    public AccessToken build() {
      return new AccessToken(this);
    }

    /**
     * Shortcut for {@code build().toJwt()}.
     *
     * @return the signed JWT
     * @throws IllegalArgumentException as {@link #build()}
     */
    public String toJwt() {
      return build().toJwt();
    }
  }
}
