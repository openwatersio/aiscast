import { describe, expect, it, vi } from "vitest";

vi.mock("../lib/media.server", () => ({ namedPhotos: async () => ({}) }));
vi.mock("../lib/fleets.server", async (real) => {
  const fleets = await real<typeof import("../lib/fleets.server")>();
  return { ...fleets, getFleet: (id: string) => (id === "superyachts" ? undefined : fleets.getFleet(id)) };
});

const { loader } = await import("./home");

describe("home loader", () => {
  it("leaves a missing fleet's slot empty, so the others keep their shape", () => {
    const { fleets } = loader({ request: new Request("https://openwaters.io/ais/vessels") } as never);
    expect(fleets.map((f) => f?.path ?? null)).toEqual(["/fleets/cruise-ships", null, "/fleets/tall-ships"]);
  });
});
