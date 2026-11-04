package io.github.keshavgoyal26.tessera.examples.spring;

import io.github.keshavgoyal26.tessera.AccessToken;
import io.github.keshavgoyal26.tessera.RoomGrant;
import java.time.Duration;
import java.util.Map;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.web.bind.annotation.PostMapping;
import org.springframework.web.bind.annotation.RequestParam;
import org.springframework.web.bind.annotation.RestController;

/**
 * Issues join tokens to the browser. A real application authenticates the user first and
 * derives the identity from its session, never from a request parameter.
 */
@RestController
class TokenController {

  private final String apiKey;
  private final String apiSecret;
  private final String url;

  TokenController(
      @Value("${tessera.api-key}") String apiKey,
      @Value("${tessera.api-secret}") String apiSecret,
      @Value("${tessera.url}") String url) {
    this.apiKey = apiKey;
    this.apiSecret = apiSecret;
    this.url = url;
  }

  @PostMapping("/api/token")
  Map<String, String> token(
      @RequestParam String room,
      @RequestParam String identity,
      @RequestParam(required = false) String name,
      @RequestParam(defaultValue = "false") boolean record) {
    String jwt =
        AccessToken.builder()
            .apiKey(apiKey)
            .secret(apiSecret)
            .identity(identity)
            .name(name == null ? identity : name)
            .ttl(Duration.ofHours(1))
            .grants(
                RoomGrant.builder()
                    .room(room)
                    .roomJoin(true)
                    // Applied only when this join auto-creates the room; selects H.264 for publishers.
                    .roomConfig(RoomGrant.RoomConfig.of(300, 20, 0, record))
                    .build())
            .toJwt();
    return Map.of("url", url, "token", jwt);
  }
}
