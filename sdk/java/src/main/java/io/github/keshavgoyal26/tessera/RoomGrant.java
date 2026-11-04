package io.github.keshavgoyal26.tessera;

import java.util.ArrayList;
import java.util.Collection;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Objects;

/**
 * Permissions carried in the {@code grants} claim of an {@link AccessToken} (ADR-0004).
 *
 * <p>Instances are immutable; create them with {@link #builder()}. Fields left at their default
 * are omitted from the token. {@code canPublish}, {@code canSubscribe} and {@code canPublishData}
 * are nullable: {@code null} means "server default" (which is {@code true}).
 *
 * <pre>{@code
 * RoomGrant grant = RoomGrant.builder()
 *     .room("standup")
 *     .roomJoin(true)
 *     .canPublishSources(List.of("camera", "microphone"))
 *     .build();
 * }</pre>
 */
public final class RoomGrant {

  /** Source name for camera tracks, for {@link Builder#canPublishSources}. */
  public static final String SOURCE_CAMERA = "camera";
  /** Source name for microphone tracks, for {@link Builder#canPublishSources}. */
  public static final String SOURCE_MICROPHONE = "microphone";
  /** Source name for screen share video, for {@link Builder#canPublishSources}. */
  public static final String SOURCE_SCREEN_SHARE = "screen_share";
  /** Source name for screen share audio, for {@link Builder#canPublishSources}. */
  public static final String SOURCE_SCREEN_SHARE_AUDIO = "screen_share_audio";

  private final String room;
  private final boolean roomJoin;
  private final boolean roomCreate;
  private final boolean roomList;
  private final boolean roomAdmin;
  private final boolean roomRecord;
  private final boolean recorderWorker;
  private final Boolean canPublish;
  private final List<String> canPublishSources;
  private final Boolean canSubscribe;
  private final Boolean canPublishData;
  private final boolean canUpdateOwnMetadata;
  private final boolean hidden;
  private final boolean recorder;
  private final RoomConfig roomConfig;

  private RoomGrant(Builder b) {
    this.room = b.room;
    this.roomJoin = b.roomJoin;
    this.roomCreate = b.roomCreate;
    this.roomList = b.roomList;
    this.roomAdmin = b.roomAdmin;
    this.roomRecord = b.roomRecord;
    this.recorderWorker = b.recorderWorker;
    this.canPublish = b.canPublish;
    this.canPublishSources = List.copyOf(b.canPublishSources);
    this.canSubscribe = b.canSubscribe;
    this.canPublishData = b.canPublishData;
    this.canUpdateOwnMetadata = b.canUpdateOwnMetadata;
    this.hidden = b.hidden;
    this.recorder = b.recorder;
    this.roomConfig = b.roomConfig;
  }

  /**
   * Returns a new builder with all fields at their defaults.
   *
   * @return a builder
   */
  public static Builder builder() {
    return new Builder();
  }

  /**
   * Returns a builder initialised with this grant's values.
   *
   * @return a builder
   */
  public Builder toBuilder() {
    Builder b = new Builder();
    b.room = room;
    b.roomJoin = roomJoin;
    b.roomCreate = roomCreate;
    b.roomList = roomList;
    b.roomAdmin = roomAdmin;
    b.roomRecord = roomRecord;
    b.recorderWorker = recorderWorker;
    b.canPublish = canPublish;
    b.canPublishSources = new ArrayList<>(canPublishSources);
    b.canSubscribe = canSubscribe;
    b.canPublishData = canPublishData;
    b.roomConfig = roomConfig;
    b.canUpdateOwnMetadata = canUpdateOwnMetadata;
    b.hidden = hidden;
    b.recorder = recorder;
    return b;
  }

  /**
   * Room the grant applies to ({@code room}).
   *
   * @return the room name, empty when unset
   */
  public String getRoom() {
    return room;
  }

  /**
   * May join {@link #getRoom()} ({@code room_join}).
   *
   * @return the flag
   */
  public boolean isRoomJoin() {
    return roomJoin;
  }

  /**
   * Admin API: create and delete rooms ({@code room_create}).
   *
   * @return the flag
   */
  public boolean isRoomCreate() {
    return roomCreate;
  }

  /**
   * Admin API: list rooms ({@code room_list}).
   *
   * @return the flag
   */
  public boolean isRoomList() {
    return roomList;
  }

  /**
   * Admin API: manage participants of the room, or all rooms when the room is empty ({@code
   * room_admin}).
   *
   * @return the flag
   */
  public boolean isRoomAdmin() {
    return roomAdmin;
  }

  /**
   * Admin API: start, stop and list recordings ({@code room_record}).
   *
   * @return the flag
   */
  public boolean isRoomRecord() {
    return roomRecord;
  }

  /**
   * Recorder worker API ({@code recorder_worker}).
   *
   * @return the flag
   */
  public boolean isRecorderWorker() {
    return recorderWorker;
  }

  /**
   * May publish media ({@code can_publish}); {@code null} means server default ({@code true}).
   *
   * @return the flag or {@code null}
   */
  public Boolean getCanPublish() {
    return canPublish;
  }

  /**
   * Allowed publish sources ({@code can_publish_sources}); empty means all sources.
   *
   * @return an immutable list
   */
  public List<String> getCanPublishSources() {
    return canPublishSources;
  }

  /**
   * May subscribe ({@code can_subscribe}); {@code null} means server default ({@code true}).
   *
   * @return the flag or {@code null}
   */
  public Boolean getCanSubscribe() {
    return canSubscribe;
  }

  /**
   * May send data packets ({@code can_publish_data}); {@code null} means server default ({@code
   * true}).
   *
   * @return the flag or {@code null}
   */
  public Boolean getCanPublishData() {
    return canPublishData;
  }

  /**
   * May update own name, metadata and attributes ({@code can_update_own_metadata}).
   *
   * @return the flag
   */
  public boolean isCanUpdateOwnMetadata() {
    return canUpdateOwnMetadata;
  }

  /**
   * Invisible to other participants ({@code hidden}).
   *
   * @return the flag
   */
  public boolean isHidden() {
    return hidden;
  }

  /**
   * Marks a recorder participant ({@code recorder}).
   *
   * @return the flag
   */
  public boolean isRecorder() {
    return recorder;
  }

  /**
   * Returns the room settings applied on auto-create.
   *
   * @return the settings or {@code null}
   */
  public RoomConfig getRoomConfig() {
    return roomConfig;
  }

  /** Room settings carried in the {@code room_config} grant field. Immutable. */
  public static final class RoomConfig {
    private final int emptyTimeoutSeconds;
    private final int departureTimeoutSeconds;
    private final int maxParticipants;
    private final boolean recordingEnabled;

    private RoomConfig(int empty, int departure, int max, boolean recording) {
      this.emptyTimeoutSeconds = empty;
      this.departureTimeoutSeconds = departure;
      this.maxParticipants = max;
      this.recordingEnabled = recording;
    }

    /**
     * Creates room settings; zero values mean "server default" and are omitted from the token.
     *
     * @param emptyTimeoutSeconds seconds an empty room stays open
     * @param departureTimeoutSeconds seconds a room stays open after the last participant left
     * @param maxParticipants participant limit
     * @param recordingEnabled whether the room records (selects H.264 for publishers)
     * @return the settings
     */
    public static RoomConfig of(
        int emptyTimeoutSeconds,
        int departureTimeoutSeconds,
        int maxParticipants,
        boolean recordingEnabled) {
      return new RoomConfig(
          emptyTimeoutSeconds, departureTimeoutSeconds, maxParticipants, recordingEnabled);
    }

    Map<String, Object> toClaims() {
      Map<String, Object> m = new LinkedHashMap<>();
      if (emptyTimeoutSeconds != 0) {
        m.put("empty_timeout_seconds", emptyTimeoutSeconds);
      }
      if (departureTimeoutSeconds != 0) {
        m.put("departure_timeout_seconds", departureTimeoutSeconds);
      }
      if (maxParticipants != 0) {
        m.put("max_participants", maxParticipants);
      }
      putIfTrue(m, "recording_enabled", recordingEnabled);
      return m;
    }
  }

  /** Returns the {@code grants} claim as an ordered map containing only non-default fields. */
  Map<String, Object> toClaims() {
    Map<String, Object> m = new LinkedHashMap<>();
    if (!room.isEmpty()) {
      m.put("room", room);
    }
    putIfTrue(m, "room_join", roomJoin);
    putIfTrue(m, "room_create", roomCreate);
    putIfTrue(m, "room_list", roomList);
    putIfTrue(m, "room_admin", roomAdmin);
    putIfTrue(m, "room_record", roomRecord);
    putIfTrue(m, "recorder_worker", recorderWorker);
    if (canPublish != null) {
      m.put("can_publish", canPublish);
    }
    if (!canPublishSources.isEmpty()) {
      m.put("can_publish_sources", canPublishSources);
    }
    if (canSubscribe != null) {
      m.put("can_subscribe", canSubscribe);
    }
    if (canPublishData != null) {
      m.put("can_publish_data", canPublishData);
    }
    putIfTrue(m, "can_update_own_metadata", canUpdateOwnMetadata);
    putIfTrue(m, "hidden", hidden);
    putIfTrue(m, "recorder", recorder);
    if (roomConfig != null) {
      Map<String, Object> rc = roomConfig.toClaims();
      if (!rc.isEmpty()) {
        m.put("room_config", rc);
      }
    }
    return m;
  }

  private static void putIfTrue(Map<String, Object> m, String key, boolean value) {
    if (value) {
      m.put(key, Boolean.TRUE);
    }
  }

  @Override
  public boolean equals(Object o) {
    return o instanceof RoomGrant other && toClaims().equals(other.toClaims());
  }

  @Override
  public int hashCode() {
    return toClaims().hashCode();
  }

  @Override
  public String toString() {
    return "RoomGrant" + toClaims();
  }

  /** Mutable builder for {@link RoomGrant}. Not thread-safe. */
  public static final class Builder {
    private String room = "";
    private boolean roomJoin;
    private boolean roomCreate;
    private boolean roomList;
    private boolean roomAdmin;
    private boolean roomRecord;
    private boolean recorderWorker;
    private Boolean canPublish;
    private List<String> canPublishSources = new ArrayList<>();
    private Boolean canSubscribe;
    private Boolean canPublishData;
    private boolean canUpdateOwnMetadata;
    private boolean hidden;
    private boolean recorder;
    private RoomConfig roomConfig;

    private Builder() {}

    /**
     * Sets the room settings applied when this token's join auto-creates the room.
     *
     * @param roomConfig the settings, or {@code null}
     * @return this builder
     */
    public Builder roomConfig(RoomConfig roomConfig) {
      this.roomConfig = roomConfig;
      return this;
    }

    /**
     * Sets the room the grant applies to.
     *
     * @param room room name; {@code null} clears it
     * @return this builder
     */
    public Builder room(String room) {
      this.room = room == null ? "" : room;
      return this;
    }

    /**
     * Allows joining the room.
     *
     * @param value the flag
     * @return this builder
     */
    public Builder roomJoin(boolean value) {
      this.roomJoin = value;
      return this;
    }

    /**
     * Allows creating and deleting rooms via the admin API.
     *
     * @param value the flag
     * @return this builder
     */
    public Builder roomCreate(boolean value) {
      this.roomCreate = value;
      return this;
    }

    /**
     * Allows listing rooms via the admin API.
     *
     * @param value the flag
     * @return this builder
     */
    public Builder roomList(boolean value) {
      this.roomList = value;
      return this;
    }

    /**
     * Allows managing participants via the admin API.
     *
     * @param value the flag
     * @return this builder
     */
    public Builder roomAdmin(boolean value) {
      this.roomAdmin = value;
      return this;
    }

    /**
     * Allows controlling recordings via the admin API.
     *
     * @param value the flag
     * @return this builder
     */
    public Builder roomRecord(boolean value) {
      this.roomRecord = value;
      return this;
    }

    /**
     * Allows calling the recorder worker API.
     *
     * @param value the flag
     * @return this builder
     */
    public Builder recorderWorker(boolean value) {
      this.recorderWorker = value;
      return this;
    }

    /**
     * Allows or forbids publishing media; {@code null} means server default.
     *
     * @param value the flag or {@code null}
     * @return this builder
     */
    public Builder canPublish(Boolean value) {
      this.canPublish = value;
      return this;
    }

    /**
     * Restricts publishable sources ({@code camera}, {@code microphone}, {@code screen_share},
     * {@code screen_share_audio}); empty or {@code null} allows all.
     *
     * @param sources the sources
     * @return this builder
     */
    public Builder canPublishSources(Collection<String> sources) {
      this.canPublishSources = new ArrayList<>();
      if (sources != null) {
        for (String s : sources) {
          this.canPublishSources.add(Objects.requireNonNull(s, "source"));
        }
      }
      return this;
    }

    /**
     * Allows or forbids subscribing; {@code null} means server default.
     *
     * @param value the flag or {@code null}
     * @return this builder
     */
    public Builder canSubscribe(Boolean value) {
      this.canSubscribe = value;
      return this;
    }

    /**
     * Allows or forbids sending data packets; {@code null} means server default.
     *
     * @param value the flag or {@code null}
     * @return this builder
     */
    public Builder canPublishData(Boolean value) {
      this.canPublishData = value;
      return this;
    }

    /**
     * Allows the participant to update its own name, metadata and attributes.
     *
     * @param value the flag
     * @return this builder
     */
    public Builder canUpdateOwnMetadata(boolean value) {
      this.canUpdateOwnMetadata = value;
      return this;
    }

    /**
     * Hides the participant from others.
     *
     * @param value the flag
     * @return this builder
     */
    public Builder hidden(boolean value) {
      this.hidden = value;
      return this;
    }

    /**
     * Marks the participant as a recorder.
     *
     * @param value the flag
     * @return this builder
     */
    public Builder recorder(boolean value) {
      this.recorder = value;
      return this;
    }

    /**
     * Builds the immutable grant.
     *
     * @return the grant
     */
    public RoomGrant build() {
      return new RoomGrant(this);
    }
  }
}
