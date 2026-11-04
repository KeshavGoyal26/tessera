package io.github.keshavgoyal26.tessera;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertThrows;

import com.sun.net.httpserver.HttpServer;
import io.github.keshavgoyal26.tessera.proto.v1.*;
import java.net.InetAddress;
import java.net.InetSocketAddress;
import java.nio.charset.StandardCharsets;
import java.time.Duration;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicInteger;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;

class RetryTest {
  HttpServer server;
  final AtomicInteger hits = new AtomicInteger();
  volatile int failFirst;
  String baseUrl;

  @BeforeEach
  void start() throws Exception {
    server = HttpServer.create(new InetSocketAddress(InetAddress.getLoopbackAddress(), 0), 0);
    server.createContext(
        "/twirp/",
        ex -> {
          ex.getRequestBody().readAllBytes();
          if (hits.incrementAndGet() <= failFirst) {
            byte[] b = "{\"code\":\"unavailable\",\"msg\":\"busy\"}".getBytes(StandardCharsets.UTF_8);
            ex.sendResponseHeaders(503, b.length);
            ex.getResponseBody().write(b);
          } else {
            byte[] b = ListRoomsResponse.getDefaultInstance().toByteArray();
            ex.sendResponseHeaders(200, b.length == 0 ? -1 : b.length);
            if (b.length > 0) ex.getResponseBody().write(b);
          }
          ex.close();
        });
    server.start();
    baseUrl = "http://127.0.0.1:" + server.getAddress().getPort();
  }

  @AfterEach
  void stop() {
    server.stop(0);
  }

  RoomServiceClient rooms(int retries) {
    return RoomServiceClient.create(
        TesseraConfig.builder()
            .baseUrl(baseUrl).apiKey("k").secret("0123456789abcdef0123456789abcdef")
            .maxRetries(retries).retryBackoff(Duration.ofMillis(5)).build());
  }

  @Test
  void idempotentCallsAreRetriedOnTransientErrors() {
    failFirst = 2;
    rooms(2).listRooms(ListRoomsRequest.getDefaultInstance());
    assertEquals(3, hits.get());
  }

  @Test
  void givesUpAfterMaxRetries() {
    failFirst = 10;
    TesseraException e =
        assertThrows(TesseraException.class, () -> rooms(2).listRooms(ListRoomsRequest.getDefaultInstance()));
    assertEquals(TesseraException.UNAVAILABLE, e.getCode());
    assertEquals(3, hits.get());
  }

  @Test
  void startRecordingIsNeverRetried() {
    failFirst = 10;
    RecordingServiceClient c =
        RecordingServiceClient.create(
            TesseraConfig.builder()
                .baseUrl(baseUrl).apiKey("k").secret("0123456789abcdef0123456789abcdef")
                .maxRetries(3).retryBackoff(Duration.ofMillis(5)).build());
    assertThrows(
        TesseraException.class,
        () -> c.startRecording(StartRecordingRequest.newBuilder().setRoom("r").build()));
    assertEquals(1, hits.get());
  }

  @Test
  void notFoundIsNotRetried() {
    RoomServiceClient c = rooms(3);
    server.removeContext("/twirp/");
    server.createContext("/twirp/", ex -> {
      hits.incrementAndGet();
      byte[] b = "{\"code\":\"not_found\",\"msg\":\"x\"}".getBytes(StandardCharsets.UTF_8);
      ex.sendResponseHeaders(404, b.length);
      ex.getResponseBody().write(b);
      ex.close();
    });
    assertThrows(TesseraException.class, () -> c.getParticipant(GetParticipantRequest.newBuilder().setRoom("r").setIdentity("i").build()));
    assertEquals(1, hits.get());
  }

  @Test
  void asyncCallCompletes() throws Exception {
    failFirst = 0;
    rooms(0).listRoomsAsync(ListRoomsRequest.getDefaultInstance()).get(5, TimeUnit.SECONDS);
  }
}
