import { describe, expect, it } from "vitest";
import { widgetFailed } from "./SlackwaterCard";

const credit = "Slackwater database (https://github.com/openwatersio/slackwater-database)";

describe("widgetFailed", () => {
  it("leaves the widget while its queries are on their way", () => {
    expect(widgetFailed({ status: "pending" })).toBe(false);
  });

  it("hides the widget when a request fails, rather than show the reader its error", () => {
    expect(widgetFailed({ status: "error" })).toBe(true);
  });

  it("hides the widget for a station the API says it can't predict, rather than show why", () => {
    expect(widgetFailed({ status: "success", data: { id: "noaa/1", predictions: false, unavailable: "No constants" } })).toBe(true);
  });

  // The widget's client parses each time into a Date.
  const curve = [{ time: new Date("2026-11-19T00:00:00Z"), level: 1 }, { time: new Date("2026-11-19T06:00:00Z"), level: 1.1 }];
  const now = Date.parse("2026-11-19T03:00:00Z");

  it("keeps the widget for a station and its credited predictions", () => {
    expect(widgetFailed({ status: "success", data: { id: "noaa/1", attribution: credit } }, now)).toBe(false);
    expect(widgetFailed({ status: "success", data: { station: { attribution: credit }, timeline: curve } }, now)).toBe(false);
    // The highs and lows draw nothing alone; the credit printed is the curve's.
    expect(widgetFailed({ status: "success", data: { station: {}, extremes: [] } }, now)).toBe(false);
  });

  it.each([undefined, "", "  ", 7])("hides predictions whose credit is %j, since their licence asks for it beside them", (attribution) => {
    expect(widgetFailed({ status: "success", data: { station: { attribution }, timeline: curve } }, now)).toBe(true);
  });

  it.each([[[]], [curve.slice(0, 1)]])("hides an answer with no curve to draw (%j), rather than the widget's note that it has none", (timeline) => {
    expect(widgetFailed({ status: "success", data: { station: { attribution: credit }, timeline } }, now)).toBe(true);
  });

  it.each(["2026-11-18T23:00:00Z", "2026-11-19T07:00:00Z"])("hides a curve that doesn't reach the present (%s), which has no reading", (at) => {
    expect(widgetFailed({ status: "success", data: { station: { attribution: credit }, timeline: curve } }, Date.parse(at))).toBe(true);
  });
});
