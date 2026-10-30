import { test } from "node:test";
import assert from "node:assert/strict";
import { parseRoster, refreshRoster } from "./refresh-ferry-fleet.mjs";

test("BC Ferries reads the full selector rather than the paginated cards", () => {
  const html = `<a href="/on-the-ferry/our-fleet/first/FST_">First</a>
    <select name="shipCodeFilter" id="shipCodeFilter">
      <option value="ALL">All vessels</option>
      <option value="first/FST_">First</option>
      <option value="second/SND_">Island G&#818;wawis</option>
    </select>`;
  const vessels = parseRoster("bc-ferries", html);
  assert.deepEqual(vessels.map((v) => v.name), ["First", "Island G̲wawis"]);
  assert.equal(vessels[1].links["Official vessel"], "https://www.bcferries.com/on-the-ferry/our-fleet/second/SND_");
  assert.throws(() => parseRoster("bc-ferries", "<html>Maintenance</html>"), /roster/i);
  assert.throws(() => parseRoster("bc-ferries", '<select id="shipCodeFilter"><option value="ALL">All</option></select>'), /empty/i);
});

test("Hy-Line distinguishes ferry service from cruises and historical ferry work", () => {
  const item = (name, text) => `<div class="block-accordion-item "><button>${name}</button><div><p>${text}</p></div></div>`;
  const vessels = parseRoster("hy-line", [
    item("Grey Lady", "Grey Lady operates between Hyannis and Nantucket.").replace('block-accordion-item ', 'block-accordion-item  is-style-open'),
    item("Lady Martha", "She operates seasonal ferry service between Hyannis and Martha’s Vineyard."),
    item("Monhegan", "Built for the Maine State Ferry Service; operating as our Cape Cod Canal Cruises vessel."),
    item("Sea Queen II", "Operating fishing trips."),
  ].join(""));
  assert.deepEqual(vessels.map((v) => v.name), ["Grey Lady", "Lady Martha"]);
  assert.throws(() => parseRoster("hy-line", item("Unknown", "Our newest boat.")), /Unknown/);
});

test("Black Ball requires the current Coho description", () => {
  assert.equal(parseRoster("black-ball", "<h2>The M.V. Coho</h2><p>Black Ball Ferry Line operates the M.V. Coho passenger and vehicle ferry.</p>")[0].name, "Coho");
  assert.throws(() => parseRoster("black-ball", "<h2>History of Coho</h2>"), /roster/i);
});

test("refresh keeps annotations and reports additions, removals and radio changes", async () => {
  const old = { title: "Test", reviewed: "2026-01-01", vessels: [
    { name: "First", mmsi: 316000001, imo: 9332779, service: "A–B", note: "Hand edited", links: { "Official vessel": "https://example.com/first" } },
    { name: "Sold", mmsi: 316000002 },
  ] };
  const official = [{ name: "First", links: { "Official vessel": "https://example.com/first" } }, { name: "New" }];
  const api = async (path) => path.includes("?q=") ? { features: [{ properties: { mmsi: 316000003, name: "FIRST", imo: 9332779 } }] } : { properties: { mmsi: 316000001, name: "OTHER", imo: 5076949 } };
  const { draft, changes } = await refreshRoster(old, official, api);
  assert.equal(draft.reviewed, old.reviewed, "running a script is not a human review");
  assert.equal(draft.vessels[0].service, "A–B");
  assert.equal(draft.vessels[0].note, "Hand edited");
  assert.equal(draft.vessels[0].mmsi, 316000003);
  assert.equal(draft.vessels[0].ais_name, "FIRST");
  assert.equal(draft.vessels[1].mmsi, undefined, "a new name alone is not verified identity");
  assert.ok(changes.some((c) => c.includes("Removed: Sold")));
  assert.ok(changes.some((c) => c.includes("Added: New")));
  assert.ok(changes.some((c) => c.includes("316000001 → 316000003")));
  assert.equal(old.vessels[0].mmsi, 316000001, "the maintained input is untouched");
});

test("unheard or conflicting identities lose their draft MMSI; candidates need review", async () => {
  const fleet = { vessels: [{ name: "First", mmsi: 316000001, imo: 9332779 }] };
  const { draft, changes } = await refreshRoster(fleet, [{ name: "First" }], async (path) => path.includes("?q=")
    ? { features: [{ properties: { mmsi: 316000002, name: "FIRST", imo: 5076949 } }] }
    : undefined);
  assert.equal(draft.vessels[0].mmsi, undefined);
  assert.equal(draft.vessels[0].imo, 9332779);
  assert.ok(changes.some((c) => c.includes("Unresolved: First")));
  assert.ok(changes.some((c) => c.includes("316000002")));
});

test("a known abbreviated AIS name can find a changed radio through its IMO", async () => {
  const fleet = { vessels: [{ name: "Spirit of British Columbia", mmsi: 316001268, imo: 9015668, ais_name: "SPIRIT OF BC" }] };
  const paths = [];
  const { draft } = await refreshRoster(fleet, [{ name: "Spirit of British Columbia" }], async (path) => {
    paths.push(path);
    if (!path.includes("?q=")) return undefined;
    return { features: path.endsWith("SPIRIT%20OF%20BC") ? [{ properties: { mmsi: 316999999, imo: 9015668, name: "SPIRIT OF BC" } }] : [] };
  });
  assert.ok(paths.some((p) => p.endsWith("SPIRIT%20OF%20BC")));
  assert.equal(draft.vessels[0].mmsi, 316999999);
});

test("an API failure aborts the refresh rather than presenting a pruned draft", async () => {
  await assert.rejects(refreshRoster({ vessels: [{ name: "First", mmsi: 316000001 }] }, [{ name: "First" }], async () => { throw new Error("HTTP 503"); }), /503/);
});

test("malformed API responses abort rather than clearing maintained identities", async () => {
  const official = [{ name: "First" }];
  await assert.rejects(refreshRoster({ vessels: [{ name: "First", mmsi: 316000001 }] }, official, async () => ({ error: "unavailable" })), /Invalid AIS record/);
  await assert.rejects(refreshRoster({ vessels: [] }, official, async () => ({ error: "unavailable" })), /Invalid AIS search/);
  await assert.rejects(refreshRoster({ vessels: [] }, official, async () => ({ features: [{ properties: { mmsi: "bad", name: "First" } }] })), /Invalid AIS search/);
});
