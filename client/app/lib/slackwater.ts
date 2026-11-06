declare global {
  interface Window {
    plausible?: (name: string, options: { props: Record<string, string> }) => void;
  }
}

/** A click handler that counts one Slackwater handoff from a ferry or other vessel page. */
export function slackwaterHandoff(context: "ferry" | "vessel") {
  return (event: { type: string; button: number }) => {
    if ((event.type === "click" && event.button === 0) || (event.type === "auxclick" && event.button === 1)) {
      window.plausible?.("slackwater_handoff", { props: { context } });
    }
  };
}
