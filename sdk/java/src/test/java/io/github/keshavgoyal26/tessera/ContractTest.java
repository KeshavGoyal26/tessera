package io.github.keshavgoyal26.tessera;

import static org.junit.jupiter.api.Assertions.assertArrayEquals;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;

import com.google.protobuf.Message;
import com.google.protobuf.util.JsonFormat;
import io.github.keshavgoyal26.tessera.internal.Json;
import io.github.keshavgoyal26.tessera.proto.v1.*;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.time.Clock;
import java.time.Duration;
import java.time.Instant;
import java.time.ZoneOffset;
import java.util.ArrayList;
import java.util.Base64;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.TreeMap;
import javax.crypto.Mac;
import javax.crypto.spec.SecretKeySpec;
import org.junit.jupiter.api.Test;

/**
 * Contract tests against the golden fixtures in {@code testdata/contract}, produced by Go
 * ({@code go test ./pkg/admin -run Golden -update}).
 */
class ContractTest {

  static final Path DIR = Path.of("..", "..", "testdata", "contract");

  @SuppressWarnings("unchecked")
  static List<Map<String, Object>> cases(String file) throws Exception {
    return (List<Map<String, Object>>) Json.parse(Files.readString(DIR.resolve(file)));
  }

  /** Normalises numbers so Long and Double compare equal. */
  static Object norm(Object o) {
    if (o instanceof Map<?, ?> m) {
      Map<String, Object> out = new TreeMap<>();
      m.forEach((k, v) -> out.put(String.valueOf(k), norm(v)));
      return out;
    }
    if (o instanceof List<?> l) {
      List<Object> out = new ArrayList<>();
      l.forEach(v -> out.add(norm(v)));
      return out;
    }
    if (o instanceof Number n) {
      return n.doubleValue();
    }
    return o;
  }

  @SuppressWarnings("unchecked")
  static RoomGrant grantFrom(Map<String, Object> g) {
    RoomGrant.Builder b = RoomGrant.builder();
    if (g == null) {
      return b.build();
    }
    if (g.get("room") instanceof String s) b.room(s);
    if (Boolean.TRUE.equals(g.get("room_join"))) b.roomJoin(true);
    if (Boolean.TRUE.equals(g.get("room_create"))) b.roomCreate(true);
    if (Boolean.TRUE.equals(g.get("room_list"))) b.roomList(true);
    if (Boolean.TRUE.equals(g.get("room_admin"))) b.roomAdmin(true);
    if (Boolean.TRUE.equals(g.get("room_record"))) b.roomRecord(true);
    if (Boolean.TRUE.equals(g.get("recorder_worker"))) b.recorderWorker(true);
    if (g.get("can_publish") instanceof Boolean v) b.canPublish(v);
    if (g.get("can_subscribe") instanceof Boolean v) b.canSubscribe(v);
    if (g.get("can_publish_data") instanceof Boolean v) b.canPublishData(v);
    if (g.get("can_publish_sources") instanceof List<?> l) b.canPublishSources((List<String>) l);
    if (Boolean.TRUE.equals(g.get("can_update_own_metadata"))) b.canUpdateOwnMetadata(true);
    if (Boolean.TRUE.equals(g.get("hidden"))) b.hidden(true);
    if (Boolean.TRUE.equals(g.get("recorder"))) b.recorder(true);
    if (g.get("room_config") instanceof Map<?, ?> rc) {
      b.roomConfig(
          RoomGrant.RoomConfig.of(
              num(rc.get("empty_timeout_seconds")),
              num(rc.get("departure_timeout_seconds")),
              num(rc.get("max_participants")),
              Boolean.TRUE.equals(rc.get("recording_enabled"))));
    }
    return b.build();
  }

  static int num(Object o) {
    return o instanceof Number n ? n.intValue() : 0;
  }

  static byte[] hmac(String secret, String data) throws Exception {
    Mac m = Mac.getInstance("HmacSHA256");
    m.init(new SecretKeySpec(secret.getBytes(StandardCharsets.UTF_8), "HmacSHA256"));
    return m.doFinal(data.getBytes(StandardCharsets.UTF_8));
  }

  @Test
  @SuppressWarnings("unchecked")
  void javaTokensCarryTheSameClaimsAsGo() throws Exception {
    List<Map<String, Object>> cs = cases("tokens/cases.json");
    assertTrue(cs.size() >= 6);
    for (Map<String, Object> c : cs) {
      String name = (String) c.get("name");
      long now = ((Number) c.get("now")).longValue();
      AccessToken.Builder b =
          AccessToken.builder()
              .apiKey((String) c.get("api_key"))
              .secret((String) c.get("secret"))
              .ttl(Duration.ofSeconds(((Number) c.get("ttl_seconds")).longValue()))
              .clock(Clock.fixed(Instant.ofEpochSecond(now), ZoneOffset.UTC))
              .grants(grantFrom((Map<String, Object>) c.get("grants")));
      if (c.get("identity") instanceof String s) b.identity(s);
      if (c.get("name_claim") instanceof String s) b.name(s);
      if (c.get("metadata") instanceof String s) b.metadata(s);
      if (c.get("attributes") instanceof Map<?, ?> a) b.attributes((Map<String, String>) a);
      String jwt = b.toJwt();

      String[] parts = jwt.split("\\.");
      String payload = new String(Base64.getUrlDecoder().decode(parts[1]), StandardCharsets.UTF_8);
      assertEquals(norm(c.get("claims")), norm(Json.parse(payload)), name + ": claims differ");

      // Same header, and Java signs identically to Go for identical payload bytes.
      String goJwt = (String) c.get("jwt");
      String[] g = goJwt.split("\\.");
      assertEquals(g[0], parts[0], name + ": header");
      byte[] sig = hmac((String) c.get("secret"), g[0] + "." + g[1]);
      assertEquals(g[2], Base64.getUrlEncoder().withoutPadding().encodeToString(sig), name + ": go signature");
    }
  }

  @Test
  void webhookFixturesVerifyAndDecode() throws Exception {
    for (Map<String, Object> c : cases("webhook/cases.json")) {
      long t = ((Number) c.get("t")).longValue();
      WebhookReceiver r =
          new WebhookReceiver((String) c.get("key"), (String) c.get("secret"))
              .withClock(Clock.fixed(Instant.ofEpochSecond(t + 5), ZoneOffset.UTC));
      Map<String, String> h = new LinkedHashMap<>();
      h.put("Tessera-Key", (String) c.get("key"));

      byte[] json = ((String) c.get("json_body")).getBytes(StandardCharsets.UTF_8);
      h.put("Tessera-Signature", (String) c.get("json_signature"));
      h.put("Content-Type", "application/json");
      WebhookEvent fromJson = r.receive(json, h);

      byte[] pb = Base64.getDecoder().decode((String) c.get("protobuf_body_base64"));
      h.put("Tessera-Signature", (String) c.get("protobuf_signature"));
      h.put("Content-Type", "application/protobuf");
      WebhookEvent fromPb = r.receive(pb, h);

      assertEquals(fromPb, fromJson, c.get("name") + ": JSON and protobuf differ");
      assertEquals(c.get("event_id"), fromPb.getId());
      assertEquals(c.get("event_type"), fromPb.getType().name());
      if (c.containsKey("room_name")) assertEquals(c.get("room_name"), fromPb.getRoom().getName());
      if (c.containsKey("participant_identity"))
        assertEquals(c.get("participant_identity"), fromPb.getParticipant().getIdentity());
      if (c.containsKey("track_sid")) assertEquals(c.get("track_sid"), fromPb.getTrack().getSid());
      if (c.containsKey("recording_id"))
        assertEquals(c.get("recording_id"), fromPb.getRecording().getRecordingId());
      // Java re-serialises to the same protobuf bytes Go produced.
      assertArrayEquals(pb, fromPb.toByteArray());

      byte[] tampered = (new String(json, StandardCharsets.UTF_8) + " ").getBytes(StandardCharsets.UTF_8);
      h.put("Tessera-Signature", (String) c.get("json_signature"));
      assertThrows(WebhookVerificationException.class, () -> r.receive(tampered, h));
    }
  }

  static Message newRequest(String method) {
    return switch (method) {
      case "CreateRoom" -> CreateRoomRequest.getDefaultInstance();
      case "ListRooms" -> ListRoomsRequest.getDefaultInstance();
      case "DeleteRoom" -> DeleteRoomRequest.getDefaultInstance();
      case "UpdateRoomMetadata" -> UpdateRoomMetadataRequest.getDefaultInstance();
      case "ListParticipants" -> ListParticipantsRequest.getDefaultInstance();
      case "GetParticipant" -> GetParticipantRequest.getDefaultInstance();
      case "RemoveParticipant" -> RemoveParticipantRequest.getDefaultInstance();
      case "MutePublishedTrack" -> MutePublishedTrackRequest.getDefaultInstance();
      case "UpdateParticipant" -> UpdateParticipantRequest.getDefaultInstance();
      case "StartRecording" -> StartRecordingRequest.getDefaultInstance();
      case "StopRecording" -> StopRecordingRequest.getDefaultInstance();
      case "ListRecordings" -> ListRecordingsRequest.getDefaultInstance();
      case "GetRecording" -> GetRecordingRequest.getDefaultInstance();
      default -> throw new IllegalArgumentException(method);
    };
  }

  static Message newResponse(String method) {
    return switch (method) {
      case "CreateRoom" -> CreateRoomResponse.getDefaultInstance();
      case "ListRooms" -> ListRoomsResponse.getDefaultInstance();
      case "DeleteRoom" -> DeleteRoomResponse.getDefaultInstance();
      case "UpdateRoomMetadata" -> UpdateRoomMetadataResponse.getDefaultInstance();
      case "ListParticipants" -> ListParticipantsResponse.getDefaultInstance();
      case "GetParticipant" -> GetParticipantResponse.getDefaultInstance();
      case "RemoveParticipant" -> RemoveParticipantResponse.getDefaultInstance();
      case "MutePublishedTrack" -> MutePublishedTrackResponse.getDefaultInstance();
      case "UpdateParticipant" -> UpdateParticipantResponse.getDefaultInstance();
      case "StartRecording" -> StartRecordingResponse.getDefaultInstance();
      case "StopRecording" -> StopRecordingResponse.getDefaultInstance();
      case "ListRecordings" -> ListRecordingsResponse.getDefaultInstance();
      case "GetRecording" -> GetRecordingResponse.getDefaultInstance();
      default -> throw new IllegalArgumentException(method);
    };
  }

  @Test
  void twirpMessagesRoundTripWithGo() throws Exception {
    List<Map<String, Object>> cs = cases("twirp/cases.json");
    assertEquals(13, cs.size(), "every Room and Recording RPC has a fixture");
    for (Map<String, Object> c : cs) {
      String method = (String) c.get("method");
      for (String side : new String[] {"request", "response"}) {
        Message proto = side.equals("request") ? newRequest(method) : newResponse(method);
        Message.Builder fromJson = proto.newBuilderForType();
        JsonFormat.parser().merge((String) c.get(side + "_json"), fromJson);
        byte[] pb = Base64.getDecoder().decode((String) c.get(side + "_pb_base64"));
        Message fromPb = proto.getParserForType().parseFrom(pb);
        assertNotNull(fromPb);
        assertEquals(fromPb, fromJson.build(), method + " " + side + ": JSON != protobuf");
        // Byte equality is not asserted: protobuf does not define field order across languages.
        assertEquals(fromPb, proto.getParserForType().parseFrom(fromPb.toByteArray()));
      }
    }
  }
}
