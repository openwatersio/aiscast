import { afterEach, describe, expect, it, vi } from "vitest";
import { trackSlackwaterHandoff } from "./slackwater";

afterEach(() => vi.unstubAllGlobals());

describe("Slackwater handoff", () => {
  it.each([
    { type: "click", button: 0, detail: 1 },
    { type: "click", button: 0, detail: 0 },
    { type: "auxclick", button: 1, detail: 1 },
  ])("sends only the event name and ferry context for $type (detail $detail, button $button)", (event) => {
    const plausible = vi.fn();
    vi.stubGlobal("window", { plausible });
    trackSlackwaterHandoff(event);
    expect(plausible).toHaveBeenCalledExactlyOnceWith("slackwater_handoff", { props: { context: "ferry" } });
  });

  it("ignores right-clicks and does not require analytics for the link to work", () => {
    const plausible = vi.fn();
    vi.stubGlobal("window", { plausible });
    trackSlackwaterHandoff({ type: "auxclick", button: 2 });
    expect(plausible).not.toHaveBeenCalled();
    vi.stubGlobal("window", {});
    expect(() => trackSlackwaterHandoff({ type: "click", button: 0 })).not.toThrow();
  });
});
