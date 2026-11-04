package io.github.keshavgoyal26.tessera.examples.spring;

import static org.springframework.test.web.servlet.request.MockMvcRequestBuilders.get;
import static org.springframework.test.web.servlet.request.MockMvcRequestBuilders.post;
import static org.springframework.test.web.servlet.result.MockMvcResultMatchers.jsonPath;
import static org.springframework.test.web.servlet.result.MockMvcResultMatchers.status;

import io.github.keshavgoyal26.tessera.WebhookReceiver;
import io.github.keshavgoyal26.tessera.proto.v1.Room;
import io.github.keshavgoyal26.tessera.proto.v1.WebhookEvent;
import io.github.keshavgoyal26.tessera.proto.v1.WebhookEventType;
import org.junit.jupiter.api.Test;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.boot.webmvc.test.autoconfigure.AutoConfigureMockMvc;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.http.MediaType;
import org.springframework.test.web.servlet.MockMvc;

@SpringBootTest(
    properties = {
      "tessera.api-key=APItest",
      "tessera.api-secret=spring-example-secret-0123456789abcdef",
      "tessera.url=http://localhost:7880"
    })
@AutoConfigureMockMvc
class ApplicationTest {

  static final String SECRET = "spring-example-secret-0123456789abcdef";

  @Autowired MockMvc mvc;

  @Test
  void issuesJoinTokens() throws Exception {
    mvc.perform(post("/api/token").param("room", "demo").param("identity", "alice").param("record", "true"))
        .andExpect(status().isOk())
        .andExpect(jsonPath("$.url").value("http://localhost:7880"))
        .andExpect(jsonPath("$.token").isNotEmpty());
  }

  @Test
  void webhookRequiresValidSignatureAndDeduplicates() throws Exception {
    byte[] body =
        WebhookEvent.newBuilder()
            .setId("evt-1")
            .setType(WebhookEventType.WEBHOOK_EVENT_TYPE_ROOM_STARTED)
            .setRoom(Room.newBuilder().setName("demo"))
            .build()
            .toByteArray();
    String sig = WebhookReceiver.sign(SECRET, System.currentTimeMillis() / 1000, body);

    mvc.perform(
            post("/webhooks/tessera")
                .contentType(MediaType.APPLICATION_OCTET_STREAM)
                .header("Content-Type", "application/protobuf")
                .header("Tessera-Key", "APItest")
                .header("Tessera-Signature", "t=1,v1=00")
                .content(body))
        .andExpect(status().isUnauthorized());

    for (int i = 0; i < 2; i++) { // second delivery is a duplicate and acknowledged
      mvc.perform(
              post("/webhooks/tessera")
                  .header("Content-Type", "application/protobuf")
                  .header("Tessera-Key", "APItest")
                  .header("Tessera-Signature", sig)
                  .content(body))
          .andExpect(status().isNoContent());
    }
    mvc.perform(get("/api/events")).andExpect(jsonPath("$.length()").value(1));
  }
}
