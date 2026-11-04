package io.github.keshavgoyal26.tessera;

import static org.junit.jupiter.api.Assertions.assertEquals;

import io.github.keshavgoyal26.tessera.proto.v1.AdminProto;
import io.github.keshavgoyal26.tessera.proto.v1.RecordingProto;
import io.github.keshavgoyal26.tessera.proto.v1.SessionDescription;
import io.github.keshavgoyal26.tessera.proto.v1.SignalRequest;
import org.junit.jupiter.api.Test;

/** Smoke test that the classes generated from proto/ are present and usable. */
class GeneratedCodeTest {

  @Test
  void signalRequestRoundTrips() throws Exception {
    SignalRequest req =
        SignalRequest.newBuilder()
            .setRequestId(7)
            .setOffer(SessionDescription.newBuilder().setType("offer").setSdp("v=0").setId(1))
            .build();
    SignalRequest parsed = SignalRequest.parseFrom(req.toByteArray());
    assertEquals(SignalRequest.MessageCase.OFFER, parsed.getMessageCase());
    assertEquals("v=0", parsed.getOffer().getSdp());
  }

  @Test
  void servicesAreDescribed() {
    assertEquals(
        "tessera.v1.RoomService",
        AdminProto.getDescriptor().findServiceByName("RoomService").getFullName());
    assertEquals(
        9, AdminProto.getDescriptor().findServiceByName("RoomService").getMethods().size());
    assertEquals(
        4,
        RecordingProto.getDescriptor()
            .findServiceByName("RecordingService")
            .getMethods()
            .size());
  }
}
