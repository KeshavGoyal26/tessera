package io.github.keshavgoyal26.tessera;

import static org.junit.jupiter.api.Assertions.assertArrayEquals;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertThrows;

import io.github.keshavgoyal26.tessera.internal.Json;
import java.nio.charset.StandardCharsets;
import java.time.Clock;
import java.time.Duration;
import java.time.Instant;
import java.time.ZoneOffset;
import java.util.Base64;
import java.util.List;
import java.util.Map;
import javax.crypto.Mac;
import javax.crypto.spec.SecretKeySpec;
import org.junit.jupiter.api.Test;

class AccessTokenTest {

  static final String KEY = "APItestkey";
  static final String SECRET = "0123456789abcdef0123456789abcdef"; // 32 bytes
  static final Clock CLOCK = Clock.fixed(Instant.ofEpochSecond(1_800_000_000L), ZoneOffset.UTC);

  private static String decode(String part) {
    return new String(Base64.getUrlDecoder().decode(part), StandardCharsets.UTF_8);
  }

  @Test
  void producesSignedJwtWithExpectedClaims() throws Exception {
    String jwt =
        AccessToken.builder()
            .apiKey(KEY)
            .secret(SECRET)
            .identity("alice")
            .name("Alice \"A\"")
            .metadata("{\"team\":\"x\"}\n")
            .attribute("role", "host")
            .ttl(Duration.ofHours(1))
            .clock(CLOCK)
            .grants(
                RoomGrant.builder()
                    .room("standup")
                    .roomJoin(true)
                    .canPublish(false)
                    .canPublishSources(List.of("camera", "microphone"))
                    .build())
            .toJwt();

    String[] parts = jwt.split("\\.");
    assertEquals(3, parts.length);
    for (String p : parts) {
      assertFalse(p.contains("="), "base64url must be unpadded");
    }
    assertEquals("{\"alg\":\"HS256\",\"typ\":\"JWT\"}", decode(parts[0]));

    String payload = decode(parts[1]);
    assertEquals(
        "{\"iss\":\"APItestkey\",\"sub\":\"alice\",\"nbf\":1800000000,\"exp\":1800003600,"
            + "\"name\":\"Alice \\\"A\\\"\",\"metadata\":\"{\\\"team\\\":\\\"x\\\"}\\n\","
            + "\"attributes\":{\"role\":\"host\"},"
            + "\"grants\":{\"room\":\"standup\",\"room_join\":true,\"can_publish\":false,"
            + "\"can_publish_sources\":[\"camera\",\"microphone\"]}}",
        payload);

    // Round-trip through the parser too.
    Map<?, ?> claims = (Map<?, ?>) Json.parse(payload);
    assertEquals("Alice \"A\"", claims.get("name"));
    assertEquals(1800003600L, claims.get("exp"));

    Mac mac = Mac.getInstance("HmacSHA256");
    mac.init(new SecretKeySpec(SECRET.getBytes(StandardCharsets.UTF_8), "HmacSHA256"));
    byte[] expected =
        mac.doFinal((parts[0] + "." + parts[1]).getBytes(StandardCharsets.US_ASCII));
    assertArrayEquals(expected, Base64.getUrlDecoder().decode(parts[2]));
  }

  @Test
  void defaultsOmitOptionalClaims() {
    String jwt =
        AccessToken.builder()
            .apiKey(KEY)
            .secret(SECRET)
            .clock(CLOCK)
            .grants(RoomGrant.builder().roomList(true).build())
            .toJwt();
    String payload = decode(jwt.split("\\.")[1]);
    assertEquals(
        "{\"iss\":\"APItestkey\",\"nbf\":1800000000,\"exp\":1800021600,"
            + "\"grants\":{\"room_list\":true}}",
        payload);
  }

  @Test
  void rejectsShortSecretUnlessAllowed() {
    AccessToken.Builder b = AccessToken.builder().apiKey(KEY).secret("too-short");
    assertThrows(IllegalArgumentException.class, b::toJwt);
    b.allowShortSecret(true);
    assertEquals(3, b.toJwt().split("\\.").length);
  }

  @Test
  void validatesJoinTokens() {
    RoomGrant join = RoomGrant.builder().room("r").roomJoin(true).build();
    assertThrows(
        IllegalArgumentException.class,
        () -> AccessToken.builder().apiKey(KEY).secret(SECRET).grants(join).build());
    assertThrows(
        IllegalArgumentException.class,
        () ->
            AccessToken.builder()
                .apiKey(KEY)
                .secret(SECRET)
                .identity("a")
                .ttl(Duration.ofHours(25))
                .grants(join)
                .build());
  }

  @Test
  void jsonEscapesControlCharacters() {
    assertEquals("\"a\\u0001\\t\\\\\"", Json.write("a\u0001\t\\"));
    assertEquals("a\u0001\t\\é", Json.parse("\"a\\u0001\\t\\\\\\u00e9\""));
  }
}
