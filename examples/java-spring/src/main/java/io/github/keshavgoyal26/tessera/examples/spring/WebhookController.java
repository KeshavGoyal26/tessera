package io.github.keshavgoyal26.tessera.examples.spring;

import io.github.keshavgoyal26.tessera.WebhookReceiver;
import io.github.keshavgoyal26.tessera.WebhookVerificationException;
import io.github.keshavgoyal26.tessera.proto.v1.WebhookEvent;
import org.springframework.http.HttpHeaders;
import org.springframework.http.ResponseEntity;
import org.springframework.web.bind.annotation.PostMapping;
import org.springframework.web.bind.annotation.RequestBody;
import org.springframework.web.bind.annotation.RequestHeader;
import org.springframework.web.bind.annotation.RestController;

/** Sketch: receives Tessera webhooks. The body must be bound as raw bytes. */
@RestController
class WebhookController {

  private final WebhookReceiver receiver;

  WebhookController(WebhookReceiver receiver) {
    this.receiver = receiver;
  }

  @PostMapping("/webhooks/tessera")
  ResponseEntity<Void> receive(@RequestBody byte[] body, @RequestHeader HttpHeaders headers) {
    WebhookEvent event;
    try {
      event = receiver.receive(body, headers.toSingleValueMap());
    } catch (WebhookVerificationException e) {
      return ResponseEntity.status(401).build();
    }
    // TODO(WP3): deduplicate on event.getId() and dispatch on event.getType().
    return ResponseEntity.noContent().build();
  }
}
