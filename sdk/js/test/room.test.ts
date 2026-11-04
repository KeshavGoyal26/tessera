import { describe, expect, it } from "vitest";
import { ConnectionError, NotImplementedError, TesseraError } from "../src/errors.js";
import { ConnectionState, Room } from "../src/room/Room.js";

describe("Room (scaffold)", () => {
  it("starts disconnected and rejects connect until implemented", async () => {
    const room = new Room();
    expect(room.state).toBe(ConnectionState.Disconnected);
    await expect(Room.connect("wss://h", "t")).rejects.toBeInstanceOf(NotImplementedError);
  });
});

describe("errors", () => {
  it("carries name and code", () => {
    const err = new ConnectionError("not_allowed", "denied", { status: 401 });
    expect(err).toBeInstanceOf(TesseraError);
    expect(err.name).toBe("ConnectionError");
    expect(err.code).toBe("not_allowed");
    expect(err.status).toBe(401);
  });
});
