import { afterEach, describe, expect, it, vi } from "vitest";
import { slackwaterHandoff } from "./slackwater";

afterEach(() => vi.unstubAllGlobals());

describe("Slackwater handoff", () => {
  it.each([
    { type: "click", button: 0, detail: 1 },
    { type: "click", button: 0, detail: 0 },
    { type: "auxclick", button: 1, detail: 1 },
  ])("sends only the event name and page context for $type (detail $detail, button $button)", (event) => {
    for (const context of ["ferry", "vessel"] as const) {
      const plausible = vi.fn();
      vi.stubGlobal("window", { plausible });
      slackwaterHandoff(context)(event);
      expect(plausible).toHaveBeenCalledExactlyOnceWith("slackwater_handoff", { props: { context } });
    }
  });

  it("ignores right-clicks and does not require analytics for the link to work", () => {
    const plausible = vi.fn();
    vi.stubGlobal("window", { plausible });
    slackwaterHandoff("ferry")({ type: "auxclick", button: 2 });
    expect(plausible).not.toHaveBeenCalled();
    vi.stubGlobal("window", {});
    expect(() => slackwaterHandoff("vessel")({ type: "click", button: 0 })).not.toThrow();
  });
});
