declare global {
  interface Window {
    plausible?: (name: string, options: { props: Record<string, string> }) => void;
  }
}

export function trackSlackwaterHandoff(event: { type: string; button: number }) {
  if ((event.type === "click" && event.button === 0) || (event.type === "auxclick" && event.button === 1)) {
    window.plausible?.("slackwater_handoff", { props: { context: "ferry" } });
  }
}
