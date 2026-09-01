import { mkdtemp, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, expect, it } from "vitest";
import { Buddies } from "../src/buddies.js";

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));
async function until(cond: () => boolean, ms = 2000): Promise<void> {
  const end = Date.now() + ms;
  while (!cond()) {
    if (Date.now() > end) throw new Error("condition not met");
    await sleep(10);
  }
}

let buddies: Buddies;
afterEach(() => buddies?.stop());

it("reports the list once per change, dropping self and duplicates", async () => {
  const file = join(await mkdtemp(join(tmpdir(), "buddies-")), "signalk-buddylist-plugin.json");
  const changes: number[][] = [];
  buddies = new Buddies(file, "123456789", (m) => changes.push(m), 20);
  const save = (urns: string[], enabled = true) =>
    writeFile(file, JSON.stringify({ configuration: { buddies: urns.map((urn) => ({ urn })) }, enabled }));

  buddies.start(); // no file yet: an empty list is not a change worth reporting
  await sleep(60);
  expect(changes).toEqual([]);

  await save(["urn:mrn:imo:mmsi:258857000", "urn:mrn:imo:mmsi:258857000", "urn:mrn:imo:mmsi:123456789"]);
  await until(() => changes.length === 1);
  expect(changes[0]).toEqual([258857000]);
  expect(buddies.mmsis).toEqual([258857000]);
  await sleep(60); // unchanged file: no re-report
  expect(changes).toHaveLength(1);

  await save(["urn:mrn:imo:mmsi:258857000"], false); // plugin disabled: list empties
  await until(() => changes.length === 2);
  expect(changes[1]).toEqual([]);
});
