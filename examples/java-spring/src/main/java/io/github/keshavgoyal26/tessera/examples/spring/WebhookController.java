package io.github.keshavgoyal26.tessera.examples.spring;

import io.github.keshavgoyal26.tessera.WebhookReceiver;
import io.github.keshavgoyal26.tessera.WebhookVerificationException;
import io.github.keshavgoyal26.tessera.proto.v1.WebhookEvent;
import java.util.ArrayDeque;
import java.util.Collections;
import java.util.Deque;
import java.util.LinkedHashSet;
import java.util.List;
import java.util.Set;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.http.HttpHeaders;
import org.springframework.http.ResponseEntity;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.PostMapping;
import org.springframework.web.bind.annotation.RequestBody;
import org.springframework.web.bind.annotation.RequestHeader;
import org.springframework.web.bind.annotation.RestController;

/**
 * Receives Tessera webhooks. The body is bound as raw bytes because the signature covers the
 * exact bytes sent. Delivery is at-least-once, so events are deduplicated by id.
 */
@RestController
class WebhookController {

  private static final Logger log = LoggerFactory.getLogger(WebhookController.class);
  private static final int KEEP = 200;

  private final WebhookReceiver receiver;
  private final Set<String> seen = Collections.synchronizedSet(new LinkedHashSet<>());
  private final Deque<String> recent = new ArrayDeque<>();

  WebhookController(WebhookReceiver receiver) {
    this.receiver = receiver;
  }

  @PostMapping("/webhooks/tessera")
  ResponseEntity<Void> receive(@RequestBody byte[] body, @RequestHeader HttpHeaders headers) {
    WebhookEvent event;
    try {
      event = receiver.receive(body, headers.toSingleValueMap());
    } catch (WebhookVerificationException e) {
      log.warn("rejected webhook: {}", e.getMessage());
      return ResponseEntity.status(401).build();
    }
    if (!seen.add(event.getId())) {
      return ResponseEntity.noContent().build(); // duplicate delivery
    }
    String line =
        switch (event.getType()) {
          case WEBHOOK_EVENT_TYPE_ROOM_STARTED -> "room started: " + event.getRoom().getName();
          case WEBHOOK_EVENT_TYPE_ROOM_FINISHED -> "room finished: " + event.getRoom().getName();
          case WEBHOOK_EVENT_TYPE_PARTICIPANT_JOINED ->
              event.getParticipant().getIdentity() + " joined " + event.getRoom().getName();
          case WEBHOOK_EVENT_TYPE_PARTICIPANT_LEFT ->
              event.getParticipant().getIdentity() + " left " + event.getRoom().getName();
          case WEBHOOK_EVENT_TYPE_RECORDING_STARTED ->
              "recording started: " + event.getRecording().getRecordingId();
          case WEBHOOK_EVENT_TYPE_RECORDING_ENDED ->
              "recording ended: "
                  + event.getRecording().getRecordingId()
                  + " ("
                  + event.getRecording().getStatus()
                  + ")";
          default -> event.getType().name();
        };
    log.info("webhook: {}", line);
    synchronized (recent) {
      recent.addFirst(line);
      while (recent.size() > KEEP) {
        recent.removeLast();
      }
    }
    return ResponseEntity.noContent().build();
  }

  /** Newest first; handy for trying the example. */
  @GetMapping("/api/events")
  List<String> events() {
    synchronized (recent) {
      return List.copyOf(recent);
    }
  }
}
