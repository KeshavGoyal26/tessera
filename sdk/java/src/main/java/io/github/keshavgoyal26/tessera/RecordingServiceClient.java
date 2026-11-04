package io.github.keshavgoyal26.tessera;

import io.github.keshavgoyal26.tessera.proto.v1.GetRecordingRequest;
import io.github.keshavgoyal26.tessera.proto.v1.GetRecordingResponse;
import io.github.keshavgoyal26.tessera.proto.v1.ListRecordingsRequest;
import io.github.keshavgoyal26.tessera.proto.v1.ListRecordingsResponse;
import io.github.keshavgoyal26.tessera.proto.v1.StartRecordingRequest;
import io.github.keshavgoyal26.tessera.proto.v1.StartRecordingResponse;
import io.github.keshavgoyal26.tessera.proto.v1.StopRecordingRequest;
import io.github.keshavgoyal26.tessera.proto.v1.StopRecordingResponse;
import io.github.keshavgoyal26.tessera.internal.TwirpClient;
import java.net.http.HttpClient;
import java.time.Duration;
import java.util.Objects;

/**
 * Client for the {@code tessera.v1.RecordingService} admin API: HLS recordings.
 *
 * <p>Each call mints a short-lived (10 minute) admin token with the {@code room_record} grant and
 * sends the request as binary protobuf over the Twirp protocol. Errors are thrown as {@link
 * TesseraException} carrying the Twirp code.
 *
 * <pre>{@code
 * RecordingServiceClient recordings = RecordingServiceClient.create(
 *     "https://tessera.example.com", "APIxxxx", System.getenv("TESSERA_API_SECRET"));
 * RecordingInfo rec = recordings.startRecording(StartRecordingRequest.newBuilder()
 *     .setRoom("standup")
 *     .setParticipant(ParticipantRecordingTarget.newBuilder().setIdentity("alice"))
 *     .build()).getRecording();
 * }</pre>
 *
 * <p>Instances are immutable and thread-safe.
 */
public final class RecordingServiceClient {

  private static final String SERVICE = "RecordingService";

  private final TesseraConfig config;
  private final TwirpClient twirp;

  private RecordingServiceClient(TesseraConfig config) {
    this.config = Objects.requireNonNull(config, "config");
    this.twirp = config.twirpClient();
  }

  /**
   * Creates a client with default HTTP settings.
   *
   * @param baseUrl server URL, e.g. {@code https://tessera.example.com}
   * @param apiKey API key
   * @param secret API secret (at least 32 bytes)
   * @return the client
   * @throws IllegalArgumentException if the URL, key or secret is invalid
   */
  public static RecordingServiceClient create(String baseUrl, String apiKey, String secret) {
    return create(TesseraConfig.builder().baseUrl(baseUrl).apiKey(apiKey).secret(secret).build());
  }

  /**
   * Creates a client using the given HTTP client and per-request timeout.
   *
   * @param baseUrl server URL, e.g. {@code https://tessera.example.com}
   * @param apiKey API key
   * @param secret API secret (at least 32 bytes)
   * @param httpClient HTTP client to use
   * @param timeout per-request timeout
   * @return the client
   * @throws IllegalArgumentException if the URL, key or secret is invalid
   */
  public static RecordingServiceClient create(
      String baseUrl, String apiKey, String secret, HttpClient httpClient, Duration timeout) {
    return create(
        TesseraConfig.builder()
            .baseUrl(baseUrl)
            .apiKey(apiKey)
            .secret(secret)
            .httpClient(httpClient)
            .timeout(timeout)
            .build());
  }

  /**
   * Creates a client from a configuration.
   *
   * @param config the configuration
   * @return the client
   */
  public static RecordingServiceClient create(TesseraConfig config) {
    return new RecordingServiceClient(config);
  }

  /**
   * Returns the configuration of this client.
   *
   * @return the configuration
   */
  public TesseraConfig getConfig() {
    return config;
  }

  /**
   * Starts a recording of a track, a participant or the whole room.
   *
   * @param request the request
   * @return the response
   * @throws TesseraException with the Twirp error code on failure
   */
  public StartRecordingResponse startRecording(StartRecordingRequest request) {
    Objects.requireNonNull(request, "request");
    return twirp.call(
        SERVICE, "StartRecording", request, StartRecordingResponse.parser(), config.adminToken(RoomGrant.builder().roomRecord(true).build()));
  }

  /**
   * Stops a recording; the returned recording is usually {@code ENDING}.
   *
   * @param request the request
   * @return the response
   * @throws TesseraException with the Twirp error code on failure
   */
  public StopRecordingResponse stopRecording(StopRecordingRequest request) {
    Objects.requireNonNull(request, "request");
    return twirp.call(
        SERVICE, "StopRecording", request, StopRecordingResponse.parser(), config.adminToken(RoomGrant.builder().roomRecord(true).build()));
  }

  /**
   * Lists recordings, optionally filtered by room or to active ones.
   *
   * @param request the request
   * @return the response
   * @throws TesseraException with the Twirp error code on failure
   */
  public ListRecordingsResponse listRecordings(ListRecordingsRequest request) {
    Objects.requireNonNull(request, "request");
    return twirp.call(
        SERVICE, "ListRecordings", request, ListRecordingsResponse.parser(), config.adminToken(RoomGrant.builder().roomRecord(true).build()));
  }

  /**
   * Gets one recording by id.
   *
   * @param request the request
   * @return the response
   * @throws TesseraException with the Twirp error code on failure
   */
  public GetRecordingResponse getRecording(GetRecordingRequest request) {
    Objects.requireNonNull(request, "request");
    return twirp.call(
        SERVICE, "GetRecording", request, GetRecordingResponse.parser(), config.adminToken(RoomGrant.builder().roomRecord(true).build()));
  }

}
