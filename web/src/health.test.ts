import { describe, expect, it } from "vitest";
import { fetchHealth } from "./health";

const answer = (status: number) => (async () => new Response("", { status })) as typeof fetch;

describe("fetchHealth", () => {
  it("is ok on 200", async () => {
    expect(await fetchHealth(answer(200))).toBe("ok");
  });
  it("is down when the database is unavailable", async () => {
    expect(await fetchHealth(answer(503))).toBe("down");
  });
  it("is down when the server cannot be reached", async () => {
    const failing = (async () => {
      throw new TypeError("Failed to fetch");
    }) as typeof fetch;
    expect(await fetchHealth(failing)).toBe("down");
  });
});
