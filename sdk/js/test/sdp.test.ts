import { describe, expect, it } from "vitest";
import { msidForMid } from "../src/rtc/sdp.js";

const sdp = [
  "v=0",
  "o=- 1 2 IN IP4 0.0.0.0",
  "s=-",
  "t=0 0",
  "a=group:BUNDLE 0 1",
  "m=audio 9 UDP/TLS/RTP/SAVPF 111",
  "a=mid:0",
  "a=msid:PA_one TR_audio",
  "a=sendonly",
  "m=video 9 UDP/TLS/RTP/SAVPF 96",
  "a=mid:1",
  "a=msid:PA_two TR_video",
  "a=sendonly",
  "",
].join("\r\n");

describe("msidForMid", () => {
  it("finds the msid of the section with the given mid", () => {
    expect(msidForMid(sdp, "1")).toEqual({ streamId: "PA_two", trackId: "TR_video" });
    expect(msidForMid(sdp, "0")).toEqual({ streamId: "PA_one", trackId: "TR_audio" });
  });
  it("returns undefined for unknown mids or missing input", () => {
    expect(msidForMid(sdp, "7")).toBeUndefined();
    expect(msidForMid(undefined, "0")).toBeUndefined();
    expect(msidForMid(sdp, null)).toBeUndefined();
  });
});
