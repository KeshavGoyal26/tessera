package io.github.keshavgoyal26.tessera;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;

import io.github.keshavgoyal26.tessera.proto.v1.Room;
import io.github.keshavgoyal26.tessera.proto.v1.WebhookEvent;
import io.github.keshavgoyal26.tessera.proto.v1.WebhookEventType;
import java.nio.charset.StandardCharsets;
import java.time.Clock;
import java.time.Instant;
import java.time.ZoneOffset;
import java.util.HashMap;
import java.util.Map;
import org.junit.jupiter.api.Test;

class WebhookReceiverTest {

  static final String KEY = "APIwebhook";
  static final String SECRET = "webhook-secret-webhook-secret-0123";
  static final long NOW = 1_800_000_000L;
  static final Clock CLOCK = Clock.fixed(Instant.ofEpochSecond(NOW), ZoneOffset.UTC);

  final WebhookReceiver receiver = new WebhookReceiver(KEY, SECRET).withClock(CLOCK);

  static final WebhookEvent EVENT =
      WebhookEvent.newBuilder()
          .setId("4d1f8e8e-0000-4000-8000-000000000001")
          .setType(WebhookEventType.WEBHOOK_EVENT_TYPE_ROOM_STARTED)
          .setRoom(Room.newBuilder().setSid("RM_1").setName("standup"))
          .setCreatedAt(NOW)
          .build();

  static Map<String, String> headers(String key, String signature, String contentType) {
    Map<String, String> h = new HashMap<>();
    // Deliberately odd casing: lookup must be case-insensitive.
    h.put("tessera-key", key);
    h.put("TESSERA-SIGNATURE", signature);
    h.put("content-type", contentType);
    return h;
  }

  @Test
  void acceptsValidProtobufWebhook() {
    byte[] body = EVENT.toByteArray();
    String sig = WebhookReceiver.sign(SECRET, NOW - 10, body);
    WebhookEvent got = receiver.receive(body, headers(KEY, sig, "application/protobuf"));
    assertEquals(EVENT, got);
  }

  @Test
  void acceptsValidJsonWebhook() {
    String json =
        "{\"id\":\"e1\",\"type\":\"WEBHOOK_EVENT_TYPE_ROOM_FINISHED\","
            + "\"room\":{\"name\":\"standup\"},\"createdAt\":\"1800000000\",\"futureField\":1}";
    byte[] body = json.getBytes(StandardCharsets.UTF_8);
    String sig = WebhookReceiver.sign(SECRET, NOW, body);
    WebhookEvent got = receiver.receive(body, headers(KEY, sig, "application/json"));
    assertEquals("e1", got.getId());
    assertEquals(WebhookEventType.WEBHOOK_EVENT_TYPE_ROOM_FINISHED, got.getType());
    assertEquals("standup", got.getRoom().getName());
    assertEquals(NOW, got.getCreatedAt());
  }

  @Test
  void rejectsTamperedBody() {
    byte[] body = EVENT.toByteArray();
    String sig = WebhookReceiver.sign(SECRET, NOW, body);
    byte[] tampered = EVENT.toBuilder().setNumDropped(5).build().toByteArray();
    WebhookVerificationException e =
        assertThrows(
            WebhookVerificationException.class,
            () -> receiver.receive(tampered, headers(KEY, sig, "application/protobuf")));
    assertEquals("unauthenticated", e.getCode());
    assertEquals(401, e.getHttpStatus());
  }

  @Test
  void rejectsWrongKeyAndWrongSecret() {
    byte[] body = EVENT.toByteArray();
    String sig = WebhookReceiver.sign(SECRET, NOW, body);
    assertThrows(
        WebhookVerificationException.class,
        () -> receiver.receive(body, headers("APIother", sig, "application/protobuf")));

    String badSig = WebhookReceiver.sign("another-secret-another-secret-0123", NOW, body);
    assertThrows(
        WebhookVerificationException.class,
        () -> receiver.receive(body, headers(KEY, badSig, "application/protobuf")));
  }

  @Test
  void rejectsStaleAndFutureTimestamps() {
    byte[] body = EVENT.toByteArray();
    String stale = WebhookReceiver.sign(SECRET, NOW - 301, body);
    WebhookVerificationException e =
        assertThrows(
            WebhookVerificationException.class,
            () -> receiver.receive(body, headers(KEY, stale, "application/protobuf")));
    assertTrue(e.getMessage().contains("tolerance"));

    String future = WebhookReceiver.sign(SECRET, NOW + 301, body);
    assertThrows(
        WebhookVerificationException.class,
        () -> receiver.receive(body, headers(KEY, future, "application/protobuf")));

    // Configurable tolerance.
    WebhookReceiver lenient = receiver.withTolerance(java.time.Duration.ofMinutes(10));
    assertEquals(EVENT, lenient.receive(body, headers(KEY, stale, "application/protobuf")));
  }

  @Test
  void rejectsMissingOrMalformedSignature() {
    byte[] body = EVENT.toByteArray();
    assertThrows(
        WebhookVerificationException.class,
        () -> receiver.receive(body, headers(KEY, "garbage", "application/protobuf")));
    assertThrows(
        WebhookVerificationException.class,
        () -> receiver.receive(body, Map.of("Tessera-Key", KEY)));
  }

  @Test
  void signFormat() {
    String sig = WebhookReceiver.sign(SECRET, 42, new byte[0]);
    assertTrue(sig.matches("t=42,v1=[0-9a-f]{64}"), sig);
  }

  /** Same vector as pkg/webhook TestKnownVector in Go: keeps both signers identical. */
  @Test
  void matchesGoSignerVector() {
    assertEquals(
        "t=1700000000,v1=b8569b78799ff9e3cbff0fc2d63a33a2b57f3282abd07c37ae5e8e7d79a5f163",
        WebhookReceiver.sign("secret", 1700000000L, "{}".getBytes(StandardCharsets.UTF_8)));
  }
}
