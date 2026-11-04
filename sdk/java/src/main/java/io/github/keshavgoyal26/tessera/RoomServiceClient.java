package io.github.keshavgoyal26.tessera;

import io.github.keshavgoyal26.tessera.proto.v1.CreateRoomRequest;
import io.github.keshavgoyal26.tessera.proto.v1.CreateRoomResponse;
import io.github.keshavgoyal26.tessera.proto.v1.DeleteRoomRequest;
import io.github.keshavgoyal26.tessera.proto.v1.DeleteRoomResponse;
import io.github.keshavgoyal26.tessera.proto.v1.GetParticipantRequest;
import io.github.keshavgoyal26.tessera.proto.v1.GetParticipantResponse;
import io.github.keshavgoyal26.tessera.proto.v1.ListParticipantsRequest;
import io.github.keshavgoyal26.tessera.proto.v1.ListParticipantsResponse;
import io.github.keshavgoyal26.tessera.proto.v1.ListRoomsRequest;
import io.github.keshavgoyal26.tessera.proto.v1.ListRoomsResponse;
import io.github.keshavgoyal26.tessera.proto.v1.MutePublishedTrackRequest;
import io.github.keshavgoyal26.tessera.proto.v1.MutePublishedTrackResponse;
import io.github.keshavgoyal26.tessera.proto.v1.RemoveParticipantRequest;
import io.github.keshavgoyal26.tessera.proto.v1.RemoveParticipantResponse;
import io.github.keshavgoyal26.tessera.proto.v1.UpdateParticipantRequest;
import io.github.keshavgoyal26.tessera.proto.v1.UpdateParticipantResponse;
import io.github.keshavgoyal26.tessera.proto.v1.UpdateRoomMetadataRequest;
import io.github.keshavgoyal26.tessera.proto.v1.UpdateRoomMetadataResponse;
import io.github.keshavgoyal26.tessera.internal.TwirpClient;
import java.net.http.HttpClient;
import java.util.concurrent.CompletableFuture;
import java.util.concurrent.ForkJoinPool;
import java.time.Duration;
import java.util.Objects;

/**
 * Client for the {@code tessera.v1.RoomService} admin API: rooms and participants.
 *
 * <p>Each call mints a short-lived (10 minute) admin token with only the grant the method needs
 * ({@code room_create}, {@code room_list} or {@code room_admin} scoped to the request's room) and
 * sends the request as binary protobuf over the Twirp protocol. Errors are thrown as {@link
 * TesseraException} carrying the Twirp code.
 *
 * <pre>{@code
 * RoomServiceClient rooms = RoomServiceClient.create(
 *     "https://tessera.example.com", "APIxxxx", System.getenv("TESSERA_API_SECRET"));
 * Room room = rooms.createRoom(CreateRoomRequest.newBuilder().setName("standup").build()).getRoom();
 * }</pre>
 *
 * <p>Instances are immutable and thread-safe.
 */
public final class RoomServiceClient {

  private static final String SERVICE = "RoomService";

  private final TesseraConfig config;
  private final TwirpClient twirp;

  private RoomServiceClient(TesseraConfig config) {
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
  public static RoomServiceClient create(String baseUrl, String apiKey, String secret) {
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
  public static RoomServiceClient create(
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
  public static RoomServiceClient create(TesseraConfig config) {
    return new RoomServiceClient(config);
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
   * Creates a room, or returns the existing room with the same name. Grant: {@code room_create}.
   *
   * @param request the request
   * @return the response
   * @throws TesseraException with the Twirp error code on failure
   */
  public CreateRoomResponse createRoom(CreateRoomRequest request) {
    Objects.requireNonNull(request, "request");
    return twirp.call(
        SERVICE, "CreateRoom", request, CreateRoomResponse.parser(), config.adminToken(RoomGrant.builder().roomCreate(true).build()));
  }

  /**
   * Lists active rooms, optionally filtered by name. Grant: {@code room_list}.
   *
   * @param request the request
   * @return the response
   * @throws TesseraException with the Twirp error code on failure
   */
  public ListRoomsResponse listRooms(ListRoomsRequest request) {
    Objects.requireNonNull(request, "request");
    return twirp.call(
        SERVICE, "ListRooms", request, ListRoomsResponse.parser(), config.adminToken(RoomGrant.builder().roomList(true).build()));
  }

  /**
   * Deletes a room and disconnects everyone. Grant: {@code room_create}.
   *
   * @param request the request
   * @return the response
   * @throws TesseraException with the Twirp error code on failure
   */
  public DeleteRoomResponse deleteRoom(DeleteRoomRequest request) {
    Objects.requireNonNull(request, "request");
    return twirp.call(
        SERVICE, "DeleteRoom", request, DeleteRoomResponse.parser(), config.adminToken(RoomGrant.builder().roomCreate(true).build()));
  }

  /**
   * Updates room metadata and notifies participants. Grant: {@code room_admin}.
   *
   * @param request the request
   * @return the response
   * @throws TesseraException with the Twirp error code on failure
   */
  public UpdateRoomMetadataResponse updateRoomMetadata(UpdateRoomMetadataRequest request) {
    Objects.requireNonNull(request, "request");
    return twirp.call(
        SERVICE, "UpdateRoomMetadata", request, UpdateRoomMetadataResponse.parser(), config.adminToken(RoomGrant.builder().roomAdmin(true).room(request.getRoom()).build()));
  }

  /**
   * Lists participants of a room, including hidden ones. Grant: {@code room_admin}.
   *
   * @param request the request
   * @return the response
   * @throws TesseraException with the Twirp error code on failure
   */
  public ListParticipantsResponse listParticipants(ListParticipantsRequest request) {
    Objects.requireNonNull(request, "request");
    return twirp.call(
        SERVICE, "ListParticipants", request, ListParticipantsResponse.parser(), config.adminToken(RoomGrant.builder().roomAdmin(true).room(request.getRoom()).build()));
  }

  /**
   * Gets one participant by identity. Grant: {@code room_admin}.
   *
   * @param request the request
   * @return the response
   * @throws TesseraException with the Twirp error code on failure
   */
  public GetParticipantResponse getParticipant(GetParticipantRequest request) {
    Objects.requireNonNull(request, "request");
    return twirp.call(
        SERVICE, "GetParticipant", request, GetParticipantResponse.parser(), config.adminToken(RoomGrant.builder().roomAdmin(true).room(request.getRoom()).build()));
  }

  /**
   * Removes a participant from a room. Grant: {@code room_admin}.
   *
   * @param request the request
   * @return the response
   * @throws TesseraException with the Twirp error code on failure
   */
  public RemoveParticipantResponse removeParticipant(RemoveParticipantRequest request) {
    Objects.requireNonNull(request, "request");
    return twirp.call(
        SERVICE, "RemoveParticipant", request, RemoveParticipantResponse.parser(), config.adminToken(RoomGrant.builder().roomAdmin(true).room(request.getRoom()).build()));
  }

  /**
   * Mutes or unmutes a published track. Grant: {@code room_admin}.
   *
   * @param request the request
   * @return the response
   * @throws TesseraException with the Twirp error code on failure
   */
  public MutePublishedTrackResponse mutePublishedTrack(MutePublishedTrackRequest request) {
    Objects.requireNonNull(request, "request");
    return twirp.call(
        SERVICE, "MutePublishedTrack", request, MutePublishedTrackResponse.parser(), config.adminToken(RoomGrant.builder().roomAdmin(true).room(request.getRoom()).build()));
  }

  /**
   * Updates name, metadata, attributes or permissions of a participant; unset fields are unchanged. Grant: {@code room_admin}.
   *
   * @param request the request
   * @return the response
   * @throws TesseraException with the Twirp error code on failure
   */
  public UpdateParticipantResponse updateParticipant(UpdateParticipantRequest request) {
    Objects.requireNonNull(request, "request");
    return twirp.call(
        SERVICE, "UpdateParticipant", request, UpdateParticipantResponse.parser(), config.adminToken(RoomGrant.builder().roomAdmin(true).room(request.getRoom()).build()));
  }


  /**
   * Asynchronous variant of {@link #createRoom(CreateRoomRequest)}.
   *
   * @param request the request
   * @return a future completed with the response, or exceptionally with a {@link TesseraException}
   */
  public CompletableFuture<CreateRoomResponse> createRoomAsync(CreateRoomRequest request) {
    return CompletableFuture.supplyAsync(() -> createRoom(request), config.getHttpClient().executor().orElse(ForkJoinPool.commonPool()));
  }

  /**
   * Asynchronous variant of {@link #listRooms(ListRoomsRequest)}.
   *
   * @param request the request
   * @return a future completed with the response, or exceptionally with a {@link TesseraException}
   */
  public CompletableFuture<ListRoomsResponse> listRoomsAsync(ListRoomsRequest request) {
    return CompletableFuture.supplyAsync(() -> listRooms(request), config.getHttpClient().executor().orElse(ForkJoinPool.commonPool()));
  }

  /**
   * Asynchronous variant of {@link #deleteRoom(DeleteRoomRequest)}.
   *
   * @param request the request
   * @return a future completed with the response, or exceptionally with a {@link TesseraException}
   */
  public CompletableFuture<DeleteRoomResponse> deleteRoomAsync(DeleteRoomRequest request) {
    return CompletableFuture.supplyAsync(() -> deleteRoom(request), config.getHttpClient().executor().orElse(ForkJoinPool.commonPool()));
  }

  /**
   * Asynchronous variant of {@link #updateRoomMetadata(UpdateRoomMetadataRequest)}.
   *
   * @param request the request
   * @return a future completed with the response, or exceptionally with a {@link TesseraException}
   */
  public CompletableFuture<UpdateRoomMetadataResponse> updateRoomMetadataAsync(UpdateRoomMetadataRequest request) {
    return CompletableFuture.supplyAsync(() -> updateRoomMetadata(request), config.getHttpClient().executor().orElse(ForkJoinPool.commonPool()));
  }

  /**
   * Asynchronous variant of {@link #listParticipants(ListParticipantsRequest)}.
   *
   * @param request the request
   * @return a future completed with the response, or exceptionally with a {@link TesseraException}
   */
  public CompletableFuture<ListParticipantsResponse> listParticipantsAsync(ListParticipantsRequest request) {
    return CompletableFuture.supplyAsync(() -> listParticipants(request), config.getHttpClient().executor().orElse(ForkJoinPool.commonPool()));
  }

  /**
   * Asynchronous variant of {@link #getParticipant(GetParticipantRequest)}.
   *
   * @param request the request
   * @return a future completed with the response, or exceptionally with a {@link TesseraException}
   */
  public CompletableFuture<GetParticipantResponse> getParticipantAsync(GetParticipantRequest request) {
    return CompletableFuture.supplyAsync(() -> getParticipant(request), config.getHttpClient().executor().orElse(ForkJoinPool.commonPool()));
  }

  /**
   * Asynchronous variant of {@link #removeParticipant(RemoveParticipantRequest)}.
   *
   * @param request the request
   * @return a future completed with the response, or exceptionally with a {@link TesseraException}
   */
  public CompletableFuture<RemoveParticipantResponse> removeParticipantAsync(RemoveParticipantRequest request) {
    return CompletableFuture.supplyAsync(() -> removeParticipant(request), config.getHttpClient().executor().orElse(ForkJoinPool.commonPool()));
  }

  /**
   * Asynchronous variant of {@link #mutePublishedTrack(MutePublishedTrackRequest)}.
   *
   * @param request the request
   * @return a future completed with the response, or exceptionally with a {@link TesseraException}
   */
  public CompletableFuture<MutePublishedTrackResponse> mutePublishedTrackAsync(MutePublishedTrackRequest request) {
    return CompletableFuture.supplyAsync(() -> mutePublishedTrack(request), config.getHttpClient().executor().orElse(ForkJoinPool.commonPool()));
  }

  /**
   * Asynchronous variant of {@link #updateParticipant(UpdateParticipantRequest)}.
   *
   * @param request the request
   * @return a future completed with the response, or exceptionally with a {@link TesseraException}
   */
  public CompletableFuture<UpdateParticipantResponse> updateParticipantAsync(UpdateParticipantRequest request) {
    return CompletableFuture.supplyAsync(() -> updateParticipant(request), config.getHttpClient().executor().orElse(ForkJoinPool.commonPool()));
  }
}
