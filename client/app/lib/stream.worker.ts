/// <reference lib="webworker" />
import { Hub } from "./streamHub";

declare const self: SharedWorkerGlobalScope;

// The worker's name is the stream's URL, token included, so a tab with another token gets a
// worker, and a stream, of its own.
const hub = new Hub(self.name);

self.onconnect = (e) => hub.add(e.ports[0]!);
