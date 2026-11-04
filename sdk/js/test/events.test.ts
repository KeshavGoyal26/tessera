import { describe, expect, it, vi } from "vitest";
import { EventEmitter } from "../src/events.js";

interface Events {
  message: (text: string, n: number) => void;
  done: () => void;
}

describe("EventEmitter", () => {
  it("calls listeners in registration order with arguments", () => {
    const e = new EventEmitter<Events>();
    const calls: string[] = [];
    e.on("message", (t, n) => calls.push(`a:${t}:${n}`));
    e.on("message", (t, n) => calls.push(`b:${t}:${n}`));
    expect(e.emit("message", "hi", 1)).toBe(true);
    expect(calls).toEqual(["a:hi:1", "b:hi:1"]);
  });

  it("returns false when nobody listens", () => {
    expect(new EventEmitter<Events>().emit("done")).toBe(false);
  });

  it("removes listeners with off", () => {
    const e = new EventEmitter<Events>();
    const fn = vi.fn();
    e.on("done", fn).off("done", fn);
    e.emit("done");
    expect(fn).not.toHaveBeenCalled();
    expect(e.listenerCount("done")).toBe(0);
  });

  it("calls once listeners a single time", () => {
    const e = new EventEmitter<Events>();
    const once = vi.fn();
    const always = vi.fn();
    e.once("done", once);
    e.on("done", always);
    e.emit("done");
    e.emit("done");
    expect(once).toHaveBeenCalledTimes(1);
    expect(always).toHaveBeenCalledTimes(2);
  });

  it("removes a once listener before it fires", () => {
    const e = new EventEmitter<Events>();
    const fn = vi.fn();
    e.once("done", fn);
    e.off("done", fn);
    e.emit("done");
    expect(fn).not.toHaveBeenCalled();
  });

  it("is safe against listeners removed during emit", () => {
    const e = new EventEmitter<Events>();
    const second = vi.fn();
    e.on("done", () => e.off("done", second));
    e.on("done", second);
    e.emit("done");
    expect(second).toHaveBeenCalledTimes(1);
    e.emit("done");
    expect(second).toHaveBeenCalledTimes(1);
  });

  it("removes all listeners", () => {
    const e = new EventEmitter<Events>();
    e.on("done", vi.fn()).on("message", vi.fn());
    e.removeAllListeners("done");
    expect(e.listenerCount("done")).toBe(0);
    expect(e.listenerCount("message")).toBe(1);
    e.removeAllListeners();
    expect(e.listenerCount("message")).toBe(0);
  });
});
