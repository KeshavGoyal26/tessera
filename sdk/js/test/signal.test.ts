import { create, fromBinary, toBinary } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";
import {
  TrackSource,
  TrackType,
  VideoQuality,
} from "../src/gen/tessera/v1/models_pb.js";
import {
  SignalRequestSchema,
  SignalResponseSchema,
} from "../src/gen/tessera/v1/signal_pb.js";
import { buildSignalUrl, decodeResponse, encodeRequest } from "../src/signal/SignalClient.js";
import { version } from "../src/version.js";
import pkg from "../package.json" with { type: "json" };

describe("buildSignalUrl", () => {
  it("builds the join URL", () => {
    const url = new URL(buildSignalUrl("wss://sfu.example.com", "tok en", { autoSubscribe: true }));
    expect(url.origin + url.pathname).toBe("wss://sfu.example.com/rtc");
    expect([...url.searchParams]).toEqual([
      ["access_token", "tok en"],
      ["protocol", "1"],
      ["auto_subscribe", "1"],
      ["sdk", "js"],
      ["version", version],
    ]);
  });

  it("maps http(s) to ws(s) and keeps a base path", () => {
    expect(buildSignalUrl("https://h/base/", "t", { autoSubscribe: false })).toBe(
      `wss://h/base/rtc?access_token=t&protocol=1&auto_subscribe=0&sdk=js&version=${version}`,
    );
    expect(buildSignalUrl("http://localhost:7880", "t", { autoSubscribe: true })).toMatch(
      /^ws:\/\/localhost:7880\/rtc\?/,
    );
  });

  it("adds resume parameters", () => {
    const url = new URL(
      buildSignalUrl("ws://h", "t", { autoSubscribe: true, protocol: 2, reconnect: true, sid: "PA_1" }),
    );
    expect(url.searchParams.get("protocol")).toBe("2");
    expect(url.searchParams.get("reconnect")).toBe("1");
    expect(url.searchParams.get("sid")).toBe("PA_1");
  });

  it("rejects invalid input", () => {
    expect(() => buildSignalUrl("not a url", "t", { autoSubscribe: true })).toThrow(/invalid/);
    expect(() => buildSignalUrl("ftp://h", "t", { autoSubscribe: true })).toThrow(/scheme/);
    expect(() => buildSignalUrl("ws://h", "t", { autoSubscribe: true, reconnect: true })).toThrow(
      /sid/,
    );
  });
});

describe("protobuf encoding", () => {
  it("round-trips a SignalRequest with an AddTrackRequest", () => {
    const req = create(SignalRequestSchema, {
      requestId: 7,
      message: {
        case: "addTrack",
        value: {
          cid: "track-1",
          name: "camera",
          type: TrackType.VIDEO,
          source: TrackSource.CAMERA,
          width: 1280,
          height: 720,
          layers: [
            { quality: VideoQuality.LOW, width: 320, height: 180, rid: "q" },
            { quality: VideoQuality.HIGH, width: 1280, height: 720, rid: "f" },
          ],
        },
      },
    });
    const bytes = encodeRequest(req);
    expect(bytes).toBeInstanceOf(Uint8Array);
    const decoded = fromBinary(SignalRequestSchema, bytes);
    expect(decoded).toEqual(req);
    expect(decoded.message.case).toBe("addTrack");
  });

  it("decodes a SignalResponse with a JoinResponse", () => {
    const res = create(SignalResponseSchema, {
      message: {
        case: "join",
        value: {
          room: { sid: "RM_1", name: "demo" },
          participant: { sid: "PA_1", identity: "alice", attributes: { role: "host" } },
          serverInfo: { version: "0.1.0", protocol: 1, minProtocol: 1 },
          iceServers: [{ urls: ["stun:stun.example.com:3478"] }],
          pingInterval: 5,
          pingTimeout: 15,
        },
      },
    });
    const bytes = toBinary(SignalResponseSchema, res);
    for (const input of [bytes, bytes.slice().buffer]) {
      const decoded = decodeResponse(input);
      expect(decoded).toEqual(res);
      if (decoded.message.case !== "join") throw new Error("expected join");
      expect(decoded.message.value.participant?.attributes).toEqual({ role: "host" });
    }
  });
});

describe("version", () => {
  it("matches package.json", () => {
    expect(version).toBe(pkg.version);
  });
});
