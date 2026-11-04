package io.github.keshavgoyal26.tessera;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;

import io.github.keshavgoyal26.tessera.proto.v1.*;
import org.junit.jupiter.api.Assumptions;
import org.junit.jupiter.api.Test;

/**
 * Runs against a real {@code tessera --dev} server. Enabled by setting {@code TESSERA_URL}
 * (default key {@code devkey}, default dev secret): {@code TESSERA_URL=http://localhost:7880 mvn test}.
 */
class LiveServerTest {

  static final String URL = System.getenv("TESSERA_URL");
  static final String KEY = System.getenv().getOrDefault("TESSERA_API_KEY", "devkey");
  static final String SECRET =
      System.getenv().getOrDefault("TESSERA_API_SECRET", "devsecret-devsecret-devsecret-0000");

  @Test
  void roomLifecycleAgainstRealServer() {
    Assumptions.assumeTrue(URL != null, "TESSERA_URL not set");
    RoomServiceClient rooms = RoomServiceClient.create(URL, KEY, SECRET);
    String name = "java-live-" + System.nanoTime();
    Room room = rooms.createRoom(CreateRoomRequest.newBuilder().setName(name).setMaxParticipants(5).build()).getRoom();
    assertEquals(name, room.getName());
    assertTrue(
        rooms.listRooms(ListRoomsRequest.newBuilder().addNames(name).build()).getRoomsList().stream()
            .anyMatch(r -> r.getName().equals(name)));
    assertEquals(
        0,
        rooms.listParticipants(ListParticipantsRequest.newBuilder().setRoom(name).build()).getParticipantsCount());
    rooms.deleteRoom(DeleteRoomRequest.newBuilder().setRoom(name).build());
    TesseraException e =
        assertThrows(
            TesseraException.class,
            () -> rooms.listParticipants(ListParticipantsRequest.newBuilder().setRoom(name).build()));
    assertEquals(TesseraException.NOT_FOUND, e.getCode());
  }

  @Test
  void javaMintedJoinTokenIsAcceptedBySignaling() throws Exception {
    Assumptions.assumeTrue(URL != null, "TESSERA_URL not set");
    String jwt =
        AccessToken.builder().apiKey(KEY).secret(SECRET).identity("java-user")
            .grants(RoomGrant.builder().room("java-join").roomJoin(true).build()).toJwt();
    // An HTTP GET without the websocket upgrade is answered by the signaling endpoint: a valid
    // token passes authentication (400 upgrade required), an invalid one is rejected (401).
    var http = java.net.http.HttpClient.newHttpClient();
    String base = URL.replaceFirst("^ws", "http");
    int ok = http.send(java.net.http.HttpRequest.newBuilder(java.net.URI.create(base + "/rtc?protocol=1&access_token=" + jwt)).build(),
        java.net.http.HttpResponse.BodyHandlers.discarding()).statusCode();
    int bad = http.send(java.net.http.HttpRequest.newBuilder(java.net.URI.create(base + "/rtc?protocol=1&access_token=junk")).build(),
        java.net.http.HttpResponse.BodyHandlers.discarding()).statusCode();
    assertEquals(401, bad);
    assertTrue(ok != 401 && ok != 403, "valid Java-minted token must pass authentication, got " + ok);
  }
}
