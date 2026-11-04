package io.github.keshavgoyal26.tessera.examples.spring;

import io.github.keshavgoyal26.tessera.RecordingServiceClient;
import io.github.keshavgoyal26.tessera.RoomServiceClient;
import io.github.keshavgoyal26.tessera.TesseraException;
import io.github.keshavgoyal26.tessera.proto.v1.CreateRoomRequest;
import io.github.keshavgoyal26.tessera.proto.v1.DeleteRoomRequest;
import io.github.keshavgoyal26.tessera.proto.v1.ListParticipantsRequest;
import io.github.keshavgoyal26.tessera.proto.v1.ListRecordingsRequest;
import io.github.keshavgoyal26.tessera.proto.v1.ListRoomsRequest;
import io.github.keshavgoyal26.tessera.proto.v1.RecordingInfo;
import io.github.keshavgoyal26.tessera.proto.v1.RemoveParticipantRequest;
import io.github.keshavgoyal26.tessera.proto.v1.RoomCompositeTarget;
import io.github.keshavgoyal26.tessera.proto.v1.StartRecordingRequest;
import io.github.keshavgoyal26.tessera.proto.v1.StopRecordingRequest;
import java.util.List;
import java.util.Map;
import org.springframework.http.HttpStatus;
import org.springframework.http.ResponseEntity;
import org.springframework.web.bind.annotation.DeleteMapping;
import org.springframework.web.bind.annotation.ExceptionHandler;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.PathVariable;
import org.springframework.web.bind.annotation.PostMapping;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.RequestParam;
import org.springframework.web.bind.annotation.RestController;

/**
 * Room and recording management through the Tessera admin API. A real application protects these
 * endpoints with its own authorization.
 */
@RestController
@RequestMapping("/api")
class AdminController {

  private final RoomServiceClient rooms;
  private final RecordingServiceClient recordings;

  AdminController(RoomServiceClient rooms, RecordingServiceClient recordings) {
    this.rooms = rooms;
    this.recordings = recordings;
  }

  @PostMapping("/rooms/{name}")
  Map<String, Object> createRoom(
      @PathVariable String name, @RequestParam(defaultValue = "0") int maxParticipants) {
    var room =
        rooms
            .createRoom(
                CreateRoomRequest.newBuilder()
                    .setName(name)
                    .setMaxParticipants(maxParticipants)
                    .build())
            .getRoom();
    return Map.of("name", room.getName(), "sid", room.getSid());
  }

  @GetMapping("/rooms")
  List<String> listRooms() {
    return rooms.listRooms(ListRoomsRequest.getDefaultInstance()).getRoomsList().stream()
        .map(r -> r.getName() + " (" + r.getNumParticipants() + ")")
        .toList();
  }

  @DeleteMapping("/rooms/{name}")
  ResponseEntity<Void> deleteRoom(@PathVariable String name) {
    rooms.deleteRoom(DeleteRoomRequest.newBuilder().setRoom(name).build());
    return ResponseEntity.noContent().build();
  }

  @GetMapping("/rooms/{name}/participants")
  List<String> participants(@PathVariable String name) {
    return rooms
        .listParticipants(ListParticipantsRequest.newBuilder().setRoom(name).build())
        .getParticipantsList()
        .stream()
        .map(p -> p.getIdentity())
        .toList();
  }

  @DeleteMapping("/rooms/{name}/participants/{identity}")
  ResponseEntity<Void> kick(@PathVariable String name, @PathVariable String identity) {
    rooms.removeParticipant(
        RemoveParticipantRequest.newBuilder().setRoom(name).setIdentity(identity).build());
    return ResponseEntity.noContent().build();
  }

  @PostMapping("/rooms/{name}/recordings")
  Map<String, String> startRecording(@PathVariable String name) {
    RecordingInfo rec =
        recordings
            .startRecording(
                StartRecordingRequest.newBuilder()
                    .setRoom(name)
                    .setRoomComposite(RoomCompositeTarget.getDefaultInstance())
                    .build())
            .getRecording();
    return Map.of("recordingId", rec.getRecordingId(), "status", rec.getStatus().name());
  }

  @DeleteMapping("/recordings/{id}")
  Map<String, String> stopRecording(@PathVariable String id) {
    RecordingInfo rec =
        recordings
            .stopRecording(StopRecordingRequest.newBuilder().setRecordingId(id).build())
            .getRecording();
    return Map.of("recordingId", rec.getRecordingId(), "status", rec.getStatus().name());
  }

  @GetMapping("/recordings")
  List<String> listRecordings(@RequestParam(defaultValue = "") String room) {
    return recordings
        .listRecordings(ListRecordingsRequest.newBuilder().setRoom(room).build())
        .getRecordingsList()
        .stream()
        .map(r -> r.getRecordingId() + " " + r.getRoom() + " " + r.getStatus())
        .toList();
  }

  /** Maps SDK errors to HTTP statuses so clients see the Tessera error code. */
  @ExceptionHandler(TesseraException.class)
  ResponseEntity<Map<String, String>> onTessera(TesseraException e) {
    int status = e.getHttpStatus() > 0 ? e.getHttpStatus() : HttpStatus.BAD_GATEWAY.value();
    return ResponseEntity.status(status).body(Map.of("code", e.getCode(), "message", String.valueOf(e.getMessage())));
  }
}
