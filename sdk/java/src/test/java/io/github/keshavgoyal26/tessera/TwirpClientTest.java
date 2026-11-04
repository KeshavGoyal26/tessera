package io.github.keshavgoyal26.tessera;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;

import com.sun.net.httpserver.HttpServer;
import io.github.keshavgoyal26.tessera.internal.Json;
import io.github.keshavgoyal26.tessera.internal.TwirpClient;
import io.github.keshavgoyal26.tessera.proto.v1.CreateRoomRequest;
import io.github.keshavgoyal26.tessera.proto.v1.CreateRoomResponse;
import io.github.keshavgoyal26.tessera.proto.v1.GetParticipantRequest;
import io.github.keshavgoyal26.tessera.proto.v1.Room;
import java.io.IOException;
import java.io.OutputStream;
import java.net.InetAddress;
import java.net.InetSocketAddress;
import java.net.http.HttpClient;
import java.nio.charset.StandardCharsets;
import java.time.Duration;
import java.util.Base64;
import java.util.Map;
import java.util.concurrent.atomic.AtomicReference;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;

class TwirpClientTest {

  static final String KEY = "APIadmin";
  static final String SECRET = "admin-secret-admin-secret-admin-12";

  HttpServer server;
  String baseUrl;
  final AtomicReference<String> lastPath = new AtomicReference<>();
  final AtomicReference<String> lastAuth = new AtomicReference<>();
  final AtomicReference<String> lastContentType = new AtomicReference<>();
  final AtomicReference<byte[]> lastBody = new AtomicReference<>();

  @BeforeEach
  void start() throws IOException {
    server = HttpServer.create(new InetSocketAddress(InetAddress.getLoopbackAddress(), 0), 0);
    server.createContext(
        "/twirp/",
        exchange -> {
          lastPath.set(exchange.getRequestURI().getPath());
          lastAuth.set(exchange.getRequestHeaders().getFirst("Authorization"));
          lastContentType.set(exchange.getRequestHeaders().getFirst("Content-Type"));
          byte[] req = exchange.getRequestBody().readAllBytes();
          lastBody.set(req);
          String path = exchange.getRequestURI().getPath();
          if (path.endsWith("/CreateRoom")) {
            CreateRoomRequest in = CreateRoomRequest.parseFrom(req);
            byte[] out =
                CreateRoomResponse.newBuilder()
                    .setRoom(
                        Room.newBuilder()
                            .setSid("RM_abc")
                            .setName(in.getName())
                            .setMaxParticipants(in.getMaxParticipants()))
                    .build()
                    .toByteArray();
            exchange.getResponseHeaders().set("Content-Type", "application/protobuf");
            respond(exchange, 200, out);
          } else {
            exchange.getResponseHeaders().set("Content-Type", "application/json");
            respond(
                exchange,
                404,
                "{\"code\":\"not_found\",\"msg\":\"participant not found\",\"meta\":{\"room\":\"r1\"}}"
                    .getBytes(StandardCharsets.UTF_8));
          }
        });
    server.start();
    baseUrl = "http://127.0.0.1:" + server.getAddress().getPort() + "/";
  }

  private static void respond(com.sun.net.httpserver.HttpExchange ex, int status, byte[] body)
      throws IOException {
    ex.sendResponseHeaders(status, body.length);
    try (OutputStream os = ex.getResponseBody()) {
      os.write(body);
    }
  }

  @AfterEach
  void stop() {
    server.stop(0);
  }

  @Test
  void createRoomSuccess() {
    TwirpClient client = new TwirpClient(baseUrl, HttpClient.newHttpClient(), Duration.ofSeconds(5));
    CreateRoomRequest req =
        CreateRoomRequest.newBuilder().setName("standup").setMaxParticipants(8).build();
    CreateRoomResponse resp =
        client.call("RoomService", "CreateRoom", req, CreateRoomResponse.parser(), "tok");
    assertEquals("RM_abc", resp.getRoom().getSid());
    assertEquals("standup", resp.getRoom().getName());
    assertEquals(8, resp.getRoom().getMaxParticipants());
    assertEquals("/twirp/tessera.v1.RoomService/CreateRoom", lastPath.get());
    assertEquals("application/protobuf", lastContentType.get());
    assertEquals("Bearer tok", lastAuth.get());
  }

  @Test
  void notFoundErrorIsMapped() {
    TwirpClient client = new TwirpClient(baseUrl, HttpClient.newHttpClient(), Duration.ofSeconds(5));
    TesseraException e =
        assertThrows(
            TesseraException.class,
            () ->
                client.call(
                    "RoomService",
                    "GetParticipant",
                    GetParticipantRequest.newBuilder().setRoom("r1").setIdentity("bob").build(),
                    CreateRoomResponse.parser(),
                    null));
    assertEquals("not_found", e.getCode());
    assertEquals(404, e.getHttpStatus());
    assertEquals("participant not found", e.getMessage());
    assertEquals(Map.of("room", "r1"), e.getMeta());
  }

  @Test
  void nonTwirpErrorBodyIsMappedFromStatus() {
    TesseraException e =
        TwirpClient.errorFromResponse(503, "<html>bad gateway</html>".getBytes(StandardCharsets.UTF_8));
    assertEquals("unavailable", e.getCode());
    assertEquals(503, e.getHttpStatus());
  }

  @Test
  void roomServiceClientMintsScopedAdminToken() {
    RoomServiceClient rooms =
        RoomServiceClient.create(
            baseUrl, KEY, SECRET, HttpClient.newHttpClient(), Duration.ofSeconds(5));
    Room room = rooms.createRoom(CreateRoomRequest.newBuilder().setName("demo").build()).getRoom();
    assertEquals("demo", room.getName());
    Map<?, ?> claims = jwtClaims(lastAuth.get());
    assertEquals(KEY, claims.get("iss"));
    assertEquals(Map.of("room_create", true), claims.get("grants"));
    long ttl = (Long) claims.get("exp") - (Long) claims.get("nbf");
    assertEquals(600L, ttl);

    TesseraException e =
        assertThrows(
            TesseraException.class,
            () ->
                rooms.getParticipant(
                    GetParticipantRequest.newBuilder().setRoom("r1").setIdentity("bob").build()));
    assertEquals("not_found", e.getCode());
    assertEquals(Map.of("room", "r1", "room_admin", true), jwtClaims(lastAuth.get()).get("grants"));
    assertTrue(lastPath.get().endsWith("/tessera.v1.RoomService/GetParticipant"));
  }

  private static Map<?, ?> jwtClaims(String authHeader) {
    assertNotNull(authHeader);
    assertTrue(authHeader.startsWith("Bearer "));
    String payload = authHeader.substring(7).split("\\.")[1];
    return (Map<?, ?>)
        Json.parse(new String(Base64.getUrlDecoder().decode(payload), StandardCharsets.UTF_8));
  }
}
