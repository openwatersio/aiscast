import { data, UNSAFE_ErrorResponseImpl as ErrorResponse } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import { handleError } from "./entry.server";

describe("handleError", () => {
  const request = new Request("https://openwaters.io/ais/vessels/368168720-x?around=1,2");
  const handle = (error: unknown, req = request) => handleError(error, { request: req, params: {}, context: {} as never });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("logs a thrown error with the section of the app", () => {
    const log = vi.spyOn(console, "error").mockImplementation(() => undefined);
    handle(new TypeError("boom"));
    expect(log).toHaveBeenCalledWith(
      expect.objectContaining({ event: "app_error", source: "server", kind: "request", message: "boom", path: "/ais/vessels/*" }),
    );
  });

  it("logs the error inside a 500 React Router made from it", () => {
    const log = vi.spyOn(console, "error").mockImplementation(() => undefined);
    handle(new ErrorResponse(500, "Internal Server Error", new Error("inner"), true));
    expect(log).toHaveBeenCalledWith(expect.objectContaining({ message: "inner" }));
  });

  it("skips a response a route threw on purpose, a refused request, and an aborted one", () => {
    const log = vi.spyOn(console, "error").mockImplementation(() => undefined);
    const outage = data("The AIS API is unavailable", { status: 503 });
    handle(new ErrorResponse(outage.init!.status!, "Service Unavailable", outage.data));
    handle(new ErrorResponse(405, "Method Not Allowed", new Error("no action"), true));
    const aborted = new AbortController();
    aborted.abort();
    handle(new Error("late"), new Request("https://openwaters.io/ais/vessels", { signal: aborted.signal }));
    expect(log).not.toHaveBeenCalled();
  });
});
