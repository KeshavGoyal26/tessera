import { create, toBinary } from "@bufbuild/protobuf";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ConnectionError } from "../src/errors.js";
import { SignalRequestSchema, SignalResponseSchema, RequestResponse_Reason } from "../src/gen/tessera/v1/signal_pb.js";
import type { SignalResponse } from "../src/gen/tessera/v1/signal_pb.js";
import { fromBinary } from "@bufbuild/protobuf";
import { SignalClient } from "../src/signal/SignalClient.js";

class MockWS {
  static OPEN = 1;
  static CONNECTING = 0;
  static instances: MockWS[] = [];
  readyState = 0;
  binaryType = "";
  sent: Uint8Array[] = [];
  onopen: (() => void) | null = null;
  onmessage: ((e: { data: ArrayBuffer }) => void) | null = null;
  onclose: ((e: { code: number; reason: string }) => void) | null = null;
  onerror: (() => void) | null = null;
  constructor(public url: string, public protocol?: string) {
    MockWS.instances.push(this);
  }
  open() {
    this.readyState = 1;
    this.onopen?.();
  }
  serve(msg: SignalResponse["message"]) {
    const bin = toBinary(SignalResponseSchema, create(SignalResponseSchema, { message: msg }));
    this.onmessage?.({ data: bin.buffer.slice(bin.byteOffset, bin.byteOffset + bin.byteLength) as ArrayBuffer });
  }
  send(d: Uint8Array) {
    this.sent.push(d);
  }
  close(code = 1000) {
    this.readyState = 3;
    this.onclose?.({ code, reason: "" });
  }
}

const ws = () => MockWS.instances.at(-1)!;
const decoded = (i = -1) => fromBinary(SignalRequestSchema, ws().sent.at(i)!);

beforeEach(() => {
  MockWS.instances = [];
  vi.stubGlobal("WebSocket", MockWS);
});
afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

async function connected(c = new SignalClient()) {
  const p = c.connect("ws://h", "tok", { autoSubscribe: true });
  ws().open();
  ws().serve({ case: "join", value: { room: { name: "r" }, pingInterval: 1, pingTimeout: 3 } as never });
  const join = await p;
  return { c, join };
}

describe("SignalClient", () => {
  it("connects with the tessera.v1 subprotocol and returns the join response", async () => {
    const { join } = await connected();
    expect(join.room?.name).toBe("r");
    expect(ws().protocol).toBe("tessera.v1");
    expect(ws().url).toContain("/rtc?access_token=tok");
    expect(ws().binaryType).toBe("arraybuffer");
  });

  it("correlates requests with RequestResponse and rejects on error reasons", async () => {
    const { c } = await connected();
    const ok = c.updateMetadata({ name: "x" });
    expect(decoded().requestId).toBe(1);
    ws().serve({ case: "requestResponse", value: { requestId: 1, reason: RequestResponse_Reason.OK } as never });
    await expect(ok).resolves.toBeUndefined();

    const bad = c.updateMetadata({ name: "y" });
    ws().serve({ case: "requestResponse", value: { requestId: 2, reason: RequestResponse_Reason.NOT_ALLOWED, message: "nope" } as never });
    await expect(bad).rejects.toThrow(/NOT_ALLOWED|nope/);
  });

  it("times out requests", async () => {
    vi.useFakeTimers();
    const c = new SignalClient({ requestMs: 100 });
    const p = c.connect("ws://h", "t", { autoSubscribe: true });
    ws().open();
    ws().serve({ case: "join", value: {} as never });
    await p;
    const r = c.updateMetadata({ name: "x" });
    const assertion = expect(r).rejects.toMatchObject({ code: "timeout" });
    await vi.advanceTimersByTimeAsync(101);
    await assertion;
  });

  it("dispatches other messages and measures rtt from pong", async () => {
    const { c } = await connected();
    const got: string[] = [];
    c.onMessage = (m) => got.push(m.message.case ?? "");
    ws().serve({ case: "speakersChanged", value: { speakers: [] } as never });
    const sent = Date.now() - 40;
    ws().serve({ case: "pong", value: { lastPingTimestamp: BigInt(sent), timestamp: 0n } as never });
    expect(got).toEqual(["speakersChanged"]);
    expect(c.rtt).toBeGreaterThanOrEqual(40);
  });

  it("pings periodically and closes on ping timeout", async () => {
    vi.useFakeTimers();
    const c = new SignalClient();
    const p = c.connect("ws://h", "t", { autoSubscribe: true });
    ws().open();
    ws().serve({ case: "join", value: {} as never });
    await p;
    const closes: number[] = [];
    c.onClose = (i) => closes.push(i.code);
    c.startPing(1, 3);
    await vi.advanceTimersByTimeAsync(2100);
    expect(ws().sent.filter((b) => fromBinary(SignalRequestSchema, b).message.case === "ping").length).toBeGreaterThanOrEqual(2);
    await vi.advanceTimersByTimeAsync(3000);
    expect(closes).toEqual([4008]);
  });

  it("reports server close codes and ignores local closes", async () => {
    const { c } = await connected();
    const infos: number[] = [];
    c.onClose = (i) => infos.push(i.code);
    ws().close(4009);
    expect(infos).toEqual([4009]);

    const second = await connected();
    second.c.onClose = (i) => infos.push(i.code);
    second.c.close();
    expect(infos).toEqual([4009]);
  });

  it("fails connect when the socket closes before the join response", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ code: "unauthenticated", msg: "bad token" }), { status: 401 })));
    const c = new SignalClient();
    const p = c.connect("ws://h", "bad", { autoSubscribe: true });
    ws().close(1006);
    await expect(p).rejects.toMatchObject({ code: "not_allowed", status: 401 });
  });

  it("classifies unreachable servers and failed resumes", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => { throw new Error("net"); }));
    const c = new SignalClient();
    const p = c.connect("ws://h", "t", { autoSubscribe: true });
    ws().close(1006);
    await expect(p).rejects.toMatchObject({ code: "server_unreachable" });

    vi.stubGlobal("fetch", vi.fn(async () => new Response("{}", { status: 409 })));
    const r = c.resume("ws://h", "t", { autoSubscribe: true, sid: "PA_1" });
    ws().close(1006);
    await expect(r).rejects.toMatchObject({ code: "resume_failed" });
  });

  it("times out waiting for join", async () => {
    vi.useFakeTimers();
    const c = new SignalClient({ joinMs: 50 });
    const p = c.connect("ws://h", "t", { autoSubscribe: true });
    const assertion = expect(p).rejects.toBeInstanceOf(ConnectionError);
    await vi.advanceTimersByTimeAsync(60);
    await assertion;
  });

  it("refuses to send while closed", () => {
    expect(() => new SignalClient().sendMute("TR_1", true)).toThrow(/not open/);
  });
});
