import { EventEmitter } from "node:events";
import { mkdirSync, mkdtempSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import type { Delta, ServerAPI } from "@signalk/server-api";

export interface FakeApp extends ServerAPI {
  deltas: Delta[];
  status: string[];
  errors: string[];
  self: Record<string, unknown>;
  model: Record<string, unknown>; // full paths for getPath
  dataDir: string;
}

// The slice of ServerAPI the plugin touches, on an EventEmitter so app.on('nmea0183') works.
export function fakeApp(self: Record<string, unknown> = {}): FakeApp {
  const em = new EventEmitter() as unknown as FakeApp;
  em.deltas = [];
  em.status = [];
  em.errors = [];
  em.self = { mmsi: "123456789", ...self };
  em.model = {};
  // Nested like the real thing (<config>/plugin-config-data/<id>), so sibling files land in a per-test dir.
  em.dataDir = join(mkdtempSync(join(tmpdir(), "aiscast-")), "signalk-aiscast");
  mkdirSync(em.dataDir);
  (em as unknown as { config: unknown }).config = { version: "2.31.1" };
  em.selfId = "urn:mrn:imo:mmsi:123456789";
  em.selfContext = "vessels.urn:mrn:imo:mmsi:123456789";
  em.getSelfPath = (path: string) => em.self[path];
  em.getPath = (path: string) => em.model[path];
  em.getDataDirPath = () => em.dataDir;
  em.handleMessage = (_id: string, delta: Partial<Delta>) => {
    em.deltas.push(delta as Delta);
  };
  em.setPluginStatus = (msg: string) => {
    em.status.push(msg);
  };
  em.setPluginError = (msg: string) => {
    em.errors.push(msg);
  };
  em.debug = (() => {}) as unknown as ServerAPI["debug"];
  em.error = (() => {}) as unknown as ServerAPI["error"];
  return em;
}
