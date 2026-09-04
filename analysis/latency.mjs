#!/usr/bin/env node
// Delay from AIS broadcast to arrival on /v1/stream, overall and per source.
// Broadcast time comes from the message's UTC-second field (Timestamp on position
// reports, UtcSecond on base station reports), so the local clock must be NTP-synced.
//
//   node analysis/latency.mjs [--seconds 120] [--bbox minLat,minLon,maxLat,maxLon] [--key ak1...]
//
// Without --bbox it subscribes to everything, which needs a key with no area cap.
import { parseArgs } from "node:util";

const { values: opt } = parseArgs({
  options: {
    seconds: { type: "string", default: "120" },
    bbox: { type: "string" },
    key: { type: "string", default: process.env.AISCAST_KEY },
    url: { type: "string", default: "wss://ais.openwaters.io/v1/stream" },
  },
});

const delays = new Map(); // source -> number[]
let skipped = 0;

const ws = new WebSocket(opt.key ? `${opt.url}?key=${opt.key}` : opt.url);
ws.onopen = () => {
  const sub = { type: "subscribe" };
  if (opt.bbox) sub.bbox = [opt.bbox.split(",").map(Number)];
  ws.send(JSON.stringify(sub));
  setTimeout(report, Number(opt.seconds) * 1000);
};
ws.onerror = (e) => console.error("websocket error", e.message);
ws.onclose = (e) => { console.error(`closed ${e.code} ${e.reason}`); report(); };
ws.onmessage = ({ data }) => {
  const recv = Date.now() / 1000;
  const ev = JSON.parse(data);
  if (ev.type !== "event") return ev.type === "error" && console.error(ev);
  const sec = ev.message?.Timestamp ?? ev.message?.UtcSecond;
  if (sec == null || sec > 59) return skipped++;
  // Same rule as the server: the event's canonical time picks the minute, a stamp up to 5 s ahead of it is
  // skew, anything further is the previous minute. Only the second-of-minute is known, so delays >= 55 s alias.
  const t = Date.parse(ev.time) / 1000;
  let b = Math.floor(t / 60) * 60 + sec;
  if (b > t + 5) b -= 60;
  const d = Math.max(0, recv - b);
  (delays.get(ev.source) ?? delays.set(ev.source, []).get(ev.source)).push(d);
};

const pct = (sorted, p) => sorted[Math.ceil(p * sorted.length) - 1];
function report() {
  const rows = [["overall", [...delays.values()].flat()], ...delays.entries()]
    .sort((a, b) => b[1].length - a[1].length)
    .map(([source, ds]) => {
      const s = ds.sort((a, b) => a - b);
      return { source, n: s.length, p50: pct(s, 0.5), p90: pct(s, 0.9), p99: pct(s, 0.99), max: s.at(-1) };
    })
    .map((r) => Object.fromEntries(Object.entries(r).map(([k, v]) => [k, typeof v === "number" && k !== "n" ? v.toFixed(2) : v])));
  console.table(rows);
  console.log(`${skipped} events skipped (no timestamp)`);
  process.exit(0);
}
process.on("SIGINT", report);
