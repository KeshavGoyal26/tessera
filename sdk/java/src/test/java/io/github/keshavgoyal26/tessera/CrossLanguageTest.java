package io.github.keshavgoyal26.tessera;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;

import com.sun.net.httpserver.HttpServer;
import io.github.keshavgoyal26.tessera.proto.v1.*;
import java.io.BufferedReader;
import java.io.IOException;
import java.io.InputStreamReader;
import java.net.InetAddress;
import java.net.InetSocketAddress;
import java.nio.file.Path;
import java.util.List;
import java.util.Map;
import java.util.TreeMap;
import java.util.concurrent.BlockingQueue;
import java.util.concurrent.CompletableFuture;
import java.util.concurrent.LinkedBlockingQueue;
import java.util.concurrent.TimeUnit;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.Assumptions;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.Test;

/**
 * Runs the real Go admin API server (pkg/admin/cmd/adminfake: real router, authZ, token
 * verification and webhook notifier over in-memory fakes) and exercises the Java SDK against it.
 * Skipped when the Go toolchain is not available.
 */
class CrossLanguageTest {

  static final String KEY = "APIcross";
  static final String SECRET = "cross-language-secret-0123456789abcdef";

  static Process server;
  static HttpServer hook;
  static String baseUrl;
  static final BlockingQueue<WebhookEvent> events = new LinkedBlockingQueue<>();
  static final BlockingQueue<String> hookErrors = new LinkedBlockingQueue<>();

  @BeforeAll
  static void start() throws Exception {
    Assumptions.assumeTrue(hasGo(), "go toolchain not found; skipping cross-language test");
    WebhookReceiver receiver = new WebhookReceiver(KEY, SECRET);
    hook = HttpServer.create(new InetSocketAddress(InetAddress.getLoopbackAddress(), 0), 0);
    hook.createContext(
        "/hook",
        ex -> {
          try {
            byte[] body = ex.getRequestBody().readAllBytes();
            Map<String, String> h = new TreeMap<>(String.CASE_INSENSITIVE_ORDER);
            ex.getRequestHeaders().forEach((k, v) -> h.put(k, v.get(0)));
            events.add(receiver.receive(body, h));
            ex.sendResponseHeaders(204, -1);
          } catch (RuntimeException e) {
            hookErrors.add(e.toString());
            ex.sendResponseHeaders(400, -1);
          } finally {
            ex.close();
          }
        });
    hook.start();

    Path bin = Path.of("target", "adminfake").toAbsolutePath();
    Process build =
        new ProcessBuilder("go", "build", "-o", bin.toString(), "../../pkg/admin/cmd/adminfake")
            .inheritIO()
            .start();
    assertEquals(0, build.waitFor(), "go build of adminfake failed");
    server =
        new ProcessBuilder(
                bin.toString(),
                "-key", KEY,
                "-secret", SECRET,
                "-webhook", "http://127.0.0.1:" + hook.getAddress().getPort() + "/hook")
            .redirectError(ProcessBuilder.Redirect.INHERIT)
            .start();
    BufferedReader out = new BufferedReader(new InputStreamReader(server.getInputStream()));
    String line = out.readLine();
    assertNotNull(line, "adminfake printed nothing");
    assertTrue(line.startsWith("LISTENING "), line);
    baseUrl = "http://127.0.0.1:" + line.substring("LISTENING ".length()).trim();
  }

  @AfterAll
  static void stop() throws IOException {
    if (server != null) {
      server.getOutputStream().close();
      server.destroy();
    }
    if (hook != null) {
      hook.stop(0);
    }
  }

  static boolean hasGo() {
    try {
      Process p = new ProcessBuilder("go", "version").redirectErrorStream(true).start();
      p.getInputStream().readAllBytes();
      return p.waitFor() == 0;
    } catch (Exception e) {
      return false;
    }
  }

  @Test
  void javaMintedTokensAreAcceptedAndRoomLifecycleWorks() {
    RoomServiceClient rooms = RoomServiceClient.create(baseUrl, KEY, SECRET);
    Room room =
        rooms.createRoom(CreateRoomRequest.newBuilder().setName("life").setMaxParticipants(7).build()).getRoom();
    assertEquals("life", room.getName());
    assertEquals(7, room.getMaxParticipants());
    assertEquals(
        1, rooms.listRooms(ListRoomsRequest.newBuilder().addNames("life").build()).getRoomsCount());
    assertEquals(
        "m2",
        rooms
            .updateRoomMetadata(
                UpdateRoomMetadataRequest.newBuilder().setRoom("life").setMetadata("m2").build())
            .getRoom()
            .getMetadata());
    List<ParticipantInfo> ps =
        rooms.listParticipants(ListParticipantsRequest.newBuilder().setRoom("life").build()).getParticipantsList();
    assertEquals("alice", ps.get(0).getIdentity());
    assertTrue(
        rooms
            .mutePublishedTrack(
                MutePublishedTrackRequest.newBuilder()
                    .setRoom("life").setIdentity("alice").setTrackSid("TR_1").setMuted(true).build())
            .getTrack()
            .getMuted());
    rooms.removeParticipant(RemoveParticipantRequest.newBuilder().setRoom("life").setIdentity("alice").build());
    rooms.deleteRoom(DeleteRoomRequest.newBuilder().setRoom("life").build());
    TesseraException e =
        assertThrows(
            TesseraException.class,
            () -> rooms.deleteRoom(DeleteRoomRequest.newBuilder().setRoom("life").build()));
    assertEquals(TesseraException.NOT_FOUND, e.getCode());
    assertEquals(404, e.getHttpStatus());
  }

  @Test
  void asyncApiWorks() throws Exception {
    RoomServiceClient rooms = RoomServiceClient.create(baseUrl, KEY, SECRET);
    CompletableFuture<CreateRoomResponse> f =
        rooms.createRoomAsync(CreateRoomRequest.newBuilder().setName("async").build());
    assertEquals("async", f.get(10, TimeUnit.SECONDS).getRoom().getName());
    CompletableFuture<DeleteRoomResponse> bad =
        rooms.deleteRoomAsync(DeleteRoomRequest.newBuilder().setRoom("nope").build());
    var ex = assertThrows(java.util.concurrent.ExecutionException.class, () -> bad.get(10, TimeUnit.SECONDS));
    assertEquals(TesseraException.NOT_FOUND, ((TesseraException) ex.getCause()).getCode());
  }

  @Test
  void recordingsLifecycle() {
    RoomServiceClient rooms = RoomServiceClient.create(baseUrl, KEY, SECRET);
    RecordingServiceClient recs = RecordingServiceClient.create(baseUrl, KEY, SECRET);
    rooms.createRoom(CreateRoomRequest.newBuilder().setName("rec").build());
    RecordingInfo r =
        recs.startRecording(
                StartRecordingRequest.newBuilder()
                    .setRoom("rec")
                    .setRoomComposite(RoomCompositeTarget.newBuilder().setLayout("grid"))
                    .setHls(HLSOptions.newBuilder().setSegmentDurationSeconds(4))
                    .build())
            .getRecording();
    assertEquals(RecordingStatus.RECORDING_STATUS_ACTIVE, r.getStatus());
    assertEquals(
        1, recs.listRecordings(ListRecordingsRequest.newBuilder().setRoom("rec").build()).getRecordingsCount());
    assertEquals(
        RecordingStatus.RECORDING_STATUS_COMPLETE,
        recs.stopRecording(StopRecordingRequest.newBuilder().setRecordingId(r.getRecordingId()).build())
            .getRecording()
            .getStatus());
    assertEquals(
        "rec",
        recs.getRecording(GetRecordingRequest.newBuilder().setRecordingId(r.getRecordingId()).build())
            .getRecording()
            .getRoom());
  }

  @Test
  void wrongSecretIsRejected() {
    RoomServiceClient rooms =
        RoomServiceClient.create(baseUrl, KEY, "another-secret-another-secret-12345678");
    TesseraException e =
        assertThrows(TesseraException.class, () -> rooms.listRooms(ListRoomsRequest.getDefaultInstance()));
    assertEquals(TesseraException.UNAUTHENTICATED, e.getCode());
    assertEquals(401, e.getHttpStatus());
  }

  @Test
  void goSignedWebhooksAreAcceptedByJavaReceiver() throws Exception {
    RoomServiceClient rooms = RoomServiceClient.create(baseUrl, KEY, SECRET);
    rooms.createRoom(CreateRoomRequest.newBuilder().setName("hooked").build());
    WebhookEvent ev;
    do {
      ev = events.poll(10, TimeUnit.SECONDS);
      assertNotNull(ev, "no webhook delivered; receiver errors: " + hookErrors);
    } while (!ev.getRoom().getName().equals("hooked"));
    assertEquals(WebhookEventType.WEBHOOK_EVENT_TYPE_ROOM_STARTED, ev.getType());
    assertFalse(ev.getId().isEmpty());
    assertTrue(hookErrors.isEmpty(), hookErrors.toString());
  }
}
