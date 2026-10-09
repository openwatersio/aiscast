#!/usr/bin/env node
// Drafts a fleet's YAML from Wikidata: every current ship of an operator, owner, builder or type,
// grouped by class, each checked against the Open Waters AIS network. The draft is a start for a
// person to edit, not a fleet to publish: it flags what Wikidata and the network disagree on with
// `# check:` comments, and never overwrites a file without --force, since fleets are edited by hand.
//
//   node client/scripts/fleet-from-wikidata.mjs --operator Q929872 --title "Royal Caribbean" \
//     --summary "Every ship sailing for Royal Caribbean International, by class." \
//     --out client/app/fleets/cruise-ships/royal-caribbean.yaml
//
// The summary is written from Wikidata's facts about the operator, owner or builder (when it was
// founded, where it is based, what it is part of) and from the fleet itself (how many ships, the
// oldest and newest). --describe <file> rewrites only that summary in an existing fleet, from the
// ships it lists, leaving the rest of the hand-edited file alone:
//
//   node client/scripts/fleet-from-wikidata.mjs --operator Q929872 --describe client/app/fleets/cruise-ships/royal-caribbean.yaml
//
// --operator, --owner, --builder or --type takes a Wikidata QID. A type includes only ships with
// an IMO or MMSI; --min-length <metres> keeps those at least that long by Wikidata's length. OPENWATERS_TOKEN, when set, is
// sent to the API, which raises its request limit; without it the script paces itself under the
// anonymous 120 requests a minute.

import { existsSync, readFileSync, writeFileSync } from "node:fs";
import { parseArgs } from "node:util";
import { load } from "js-yaml";

const { values: args } = parseArgs({
  options: {
    operator: { type: "string" },
    owner: { type: "string" },
    builder: { type: "string" },
    type: { type: "string" },
    title: { type: "string" },
    summary: { type: "string" },
    describe: { type: "string" },
    out: { type: "string" },
    force: { type: "boolean", default: false },
    api: { type: "string", default: "https://ais.openwaters.io" },
    "min-length": { type: "string" },
  },
});

const by = ["operator", "owner", "builder", "type"].filter((k) => args[k]);
if (by.length !== 1 || !/^Q\d+$/.test(args[by[0]]) || !(args.title || args.describe)) {
  console.error(
    "usage: fleet-from-wikidata.mjs --operator|--owner|--builder|--type Q<id> (--title <title> [--summary <text>] [--out <file> [--force]] | --describe <file>)",
  );
  process.exit(2);
}
if (args.out && existsSync(args.out) && !args.force) {
  console.error(`${args.out} exists; fleets are edited by hand, so pass --force to replace it`);
  process.exit(1);
}

// Wikimedia asks every client to name itself and give a contact.
const USER_AGENT = "aiscast-fleets/1.0 (https://openwaters.io/ais/; hello@openwaters.io)";
const qid = args[by[0]];

// A current ship: for an operator or owner, a statement with no end date, so a ship sold on is left
// out; never retired or dissolved; and a ship, which leaves out the line's terminals and aircraft.
const MATCH = {
  operator: `?s p:P137 ?st . ?st ps:P137 wd:${qid} . FILTER NOT EXISTS { ?st pq:P582 [] }`,
  owner: `?s p:P127 ?st . ?st ps:P127 wd:${qid} . FILTER NOT EXISTS { ?st pq:P582 [] }`,
  builder: `?s wdt:P176 wd:${qid} .`,
  // A type reaches back centuries, so only ships with an IMO or MMSI, which the AIS era gave them.
  type: `?s wdt:P31/wdt:P279* wd:${qid} . FILTER EXISTS { { ?s wdt:P458 [] } UNION { ?s wdt:P587 [] } }`,
}[by[0]];

const QUERY = `
SELECT ?s ?sLabel ?mmsi ?imo ?classLabel ?inService ?length WHERE {
  ${MATCH}
  ?s wdt:P31/wdt:P279* wd:Q11446 .
  FILTER NOT EXISTS { ?s wdt:P730 [] }
  FILTER NOT EXISTS { ?s wdt:P576 [] }
  ${args["min-length"] ? `?s p:P2043/psn:P2043/wikibase:quantityAmount ?minLength . FILTER(?minLength >= ${Number(args["min-length"])})` : ""}
  OPTIONAL { ?s wdt:P587 ?mmsi }
  OPTIONAL { ?s wdt:P458 ?imo }
  OPTIONAL { ?s wdt:P289 ?class }
  OPTIONAL { ?s wdt:P729 ?inService }
  OPTIONAL { ?s p:P2043/psn:P2043/wikibase:quantityAmount ?length }
  SERVICE wikibase:label { bd:serviceParam wikibase:language "en,mul,de,fr,es,it,nl,nb,sv,fi,da" }
}`;

async function wikidata() {
  const url = new URL("https://query.wikidata.org/sparql");
  url.searchParams.set("query", QUERY);
  const res = await fetch(url, { headers: { accept: "application/sparql-results+json", "user-agent": USER_AGENT } });
  if (!res.ok) throw new Error(`Wikidata ${res.status}: ${(await res.text()).slice(0, 200)}`);
  return (await res.json()).results.bindings;
}

async function sparql(query) {
  const url = new URL("https://query.wikidata.org/sparql");
  url.searchParams.set("query", query);
  const res = await fetch(url, { headers: { accept: "application/sparql-results+json", "user-agent": USER_AGENT } });
  if (!res.ok) throw new Error(`Wikidata ${res.status}: ${(await res.text()).slice(0, 200)}`);
  return (await res.json()).results.bindings;
}

/**
 * Two sentences about the fleet: what Wikidata says of the line, yard or owner, and what the ships
 * themselves say. "Royal Caribbean International was founded in 1968, is based in Miami and is part
 * of Royal Caribbean Group. Its 30 ships range from Grandeur of the Seas (1996) to Legend of the
 * Seas (2026)." A type has no founding, so only the second.
 */
async function describe(title, ships) {
  const facts = [];
  if (by[0] !== "type") {
    const [row = {}] = await sparql(`
      SELECT ?inception ?founderLabel ?hqLabel ?parentLabel WHERE {
        OPTIONAL { wd:${qid} wdt:P571 ?inception }
        OPTIONAL { wd:${qid} wdt:P112 ?founder }
        OPTIONAL { wd:${qid} wdt:P159 ?hq }
        OPTIONAL { wd:${qid} wdt:P749 ?parent }
        SERVICE wikibase:label { bd:serviceParam wikibase:language "en,mul" }
      } LIMIT 1`);
    const v = (k) => row[k]?.value;
    const named = (s) => s && !/^Q\d+$/.test(s);
    if (v("inception")) facts.push(`was founded in ${v("inception").slice(0, 4)}${named(v("founderLabel")) ? ` by ${v("founderLabel")}` : ""}`);
    if (named(v("hqLabel"))) facts.push(`is based in ${v("hqLabel")}`);
    if (named(v("parentLabel")) && v("parentLabel") !== title) facts.push(`is part of ${v("parentLabel")}`);
  }
  const sentences = [];
  // A label that ends a sentence already, "Holdings Ltd.", takes no second full stop.
  if (facts.length) sentences.push(`${title} ${facts.length > 1 ? `${facts.slice(0, -1).join(", ")} and ${facts.at(-1)}` : facts[0]}.`.replace(/\.\.$/, "."));
  const dated = ships.filter((s) => s.year).sort((a, b) => a.year - b.year);
  const n = ships.length;
  if (dated.length > 1 && dated[0].year !== dated.at(-1).year)
    sentences.push(`Its ${n} ships range from ${dated[0].name} (${dated[0].year}) to ${dated.at(-1).name} (${dated.at(-1).year}).`);
  else if (n) sentences.push(`It sails ${n} ship${n === 1 ? "" : "s"}.`);
  return sentences.join(" ");
}

if (args.describe) {
  const text = readFileSync(args.describe, "utf8");
  const fleet = load(text);
  const vessels = (fleet.sections ?? [{ vessels: fleet.vessels }]).flatMap((s) => s.vessels);
  const ships = vessels.map((v) => ({ name: v.name, year: Number(/in service (\d{4})/.exec(v.subtitle ?? "")?.[1]) || undefined }));
  const summary = await describe(fleet.title, ships);
  writeFileSync(args.describe, text.replace(/^summary: .*$/m, `summary: ${JSON.stringify(summary)}`));
  console.error(summary);
  process.exit(0);
}

const token = process.env.OPENWATERS_TOKEN;
const pause = token ? 0 : 600;
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

async function api(path) {
  for (let attempt = 0; ; attempt++) {
    await sleep(pause);
    const res = await fetch(`${args.api}${path}`, { headers: { accept: "application/json", ...(token ? { authorization: `Bearer ${token}` } : {}) } });
    if (res.status === 404) return undefined;
    // Over the limit: wait out the minute and ask again.
    if (res.status === 429 && attempt < 3) {
      await sleep(60_000);
      continue;
    }
    if (!res.ok) throw new Error(`API ${path}: ${res.status}`);
    return res.json();
  }
}

const value = (row, key) => row[key]?.value;
const year = (iso) => iso?.slice(0, 4);
// "Oasis-class cruise ship" reads as "Oasis class".
const className = (label) => {
  // Wikidata tells same-named classes apart with a year: "Vista (2002) class".
  const m = label && /^(.*?)[- ]class\b/i.exec(label.replace(/\s*\(\d{4}\)/, ""));
  return m ? `${m[1]} class` : label && !/^Q\d+$/.test(label) ? label : undefined;
};
// A network name in capitals, as AIS sends it, reads as "Legend Of The Seas"; good enough for a
// draft whose names a person checks.
const titleCase = (s) => s.toLowerCase().replace(/\b\w/g, (c) => c.toUpperCase());
const PREFIX = /^(MS|MV|M\/V|SS|RV|R\/V)\s+/i;
// Whether a name on the air is the ship's. AIS names are capitals without accents, cut at 20
// characters, and often drop a prefix, an article or the line's name ("SKY" for Norwegian Sky);
// some lines abbreviate ("ENCHANTMENT OTS", "CELEB.CONSTELLATION").
function sameName(heard, name) {
  const norm = (s) =>
    s
      .normalize("NFD")
      .replace(/[\u0300-\u036f]/g, "")
      .toUpperCase()
      .replace(PREFIX, "")
      .replace(/^(LE|LA|THE)\s+/, "")
      .replace(/\bOTS\b/g, "OF THE SEAS")
      .replace(/[^A-Z0-9]/g, "");
  const a = norm(heard);
  const b = norm(name);
  const tail = norm(heard.split(".").pop());
  return a === b || (heard.length >= 19 && b.startsWith(a)) || (a.length >= 3 && b.endsWith(a)) || (tail.length >= 3 && b.endsWith(tail));
}

// One ship per item: Wikidata gives a row per combination of values a ship has more than one of.
const ships = new Map();
for (const row of await wikidata()) {
  const id = value(row, "s").split("/").pop();
  const ship = ships.get(id) ?? { id, name: value(row, "sLabel"), mmsis: new Set(), imo: undefined, classes: new Set() };
  if (value(row, "mmsi")) ship.mmsis.add(value(row, "mmsi"));
  ship.imo ??= value(row, "imo");
  if (className(value(row, "classLabel"))) ship.classes.add(className(value(row, "classLabel")));
  ship.inService ??= year(value(row, "inService"));
  // Wikidata's lengths come in whatever unit an editor used; the network's AIS length, when heard, wins.
  ship.wdLength ??= value(row, "length") && Math.round(Number(value(row, "length")));
  ships.set(id, ship);
}
console.error(`${ships.size} ships on Wikidata`);

// A ship with no IMO or MMSI that entered service before AIS was carried is one of the line's
// historic ships whose operator statement Wikidata never ended, not a ship in the fleet today.
for (const ship of [...ships.values()]) {
  if (!ship.imo && !ship.mmsis.size && Number(ship.inService) < 1970) {
    console.error(`left out ${ship.name} (${ship.id}), in service ${ship.inService}: historic`);
    ships.delete(ship.id);
  }
}

// Each ship checked against the network. Nothing is dropped: a ship the network has never heard is
// still in the fleet, and the page shows it without a position.
for (const ship of ships.values()) {
  ship.checks = [];
  const imo = ship.imo && /^\d{7}$/.test(ship.imo) ? Number(ship.imo) : undefined;
  if (ship.imo && !imo) ship.checks.push(`Wikidata's IMO ${ship.imo} is not seven digits`);
  ship.imo = imo;
  // An item with no name in any language Wikidata is asked for is labelled with its QID, and
  // some carry their IMO as their only name.
  const named = /^(Q\d+|IMO \d+)$/i.test(ship.name) ? undefined : ship.name;
  for (const mmsi of [...ship.mmsis].filter((m) => /^[2-7]\d{8}$/.test(m))) {
    const heard = await api(`/v1/vessels/${mmsi}`);
    const p = heard?.properties;
    if (!p) continue;
    if (imo && p.imo && p.imo !== imo) {
      ship.checks.push(`MMSI ${mmsi} reports IMO ${p.imo}, not ${imo}: another ship now has it`);
      continue;
    }
    // Without an IMO to confirm it, only the name ties the MMSI to this ship: MMSIs are reissued.
    if (!(imo && p.imo === imo) && named && p.name && !sameName(p.name, named)) {
      ship.checks.push(`MMSI ${mmsi} is heard as ${p.name}, with no IMO to say it is this ship`);
      continue;
    }
    ship.mmsi = Number(mmsi);
    ship.heardAs = p.name;
    ship.length = p.length;
    break;
  }
  // Wikidata often lacks a ship's MMSI, or has one from before a change of flag. The network's
  // vessels that report the ship's IMO under its name settle it.
  if (!ship.mmsi && imo && named) {
    const found = await api(`/v1/vessels?q=${encodeURIComponent(named.toUpperCase().replace(PREFIX, ""))}`).catch((e) => {
      ship.checks.push(`the network's search for "${named}" failed: ${e.message}`);
      return undefined;
    });
    const match = found?.features?.find((f) => f.properties.imo === imo);
    if (match) {
      ship.mmsi = match.properties.mmsi;
      ship.heardAs = match.properties.name;
      ship.length = match.properties.length;
      ship.checks.push(`MMSI from the network, matched by IMO${ship.mmsis.size ? `; Wikidata has ${[...ship.mmsis].join(", ")}` : ""}`);
    }
  }
  if (!ship.mmsi && ship.mmsis.size) ship.checks.push(`Wikidata's MMSI ${[...ship.mmsis].join(", ")} is not heard on the network`);
  if (!ship.mmsi && !ship.mmsis.size) ship.checks.push("no MMSI on Wikidata or the network");
  // A different name on the air is most often a ship sold on that Wikidata still lists.
  if (ship.heardAs && named && !sameName(ship.heardAs, named)) ship.checks.push(`heard as ${ship.heardAs}: renamed or sold?`);
  ship.length ??= ship.wdLength;
  if (ship.length > 500) {
    ship.checks.push(`Wikidata's length ${ship.length} is not in metres`);
    ship.length = undefined;
  }
  ship.name = (named ?? (ship.heardAs ? titleCase(ship.heardAs) : ship.id)).replace(PREFIX, "");
  if (!named) ship.checks.push(`no English name on Wikidata; named from ${ship.heardAs ? "the network" : "its QID"}`);
  if (ship.classes.size > 1) ship.checks.push(`Wikidata gives several classes: ${[...ship.classes].join(", ")}`);
}

// Sections by class, newest class first, and ships newest first within each; ships without a
// class last.
const sections = new Map();
for (const ship of ships.values()) {
  const key = [...ship.classes][0] ?? "";
  sections.set(key, [...(sections.get(key) ?? []), ship]);
}
const newest = (list) => Math.max(...list.map((s) => Number(s.inService ?? 0)));
const ordered = [...sections.entries()]
  .sort(([a, x], [b, y]) => (a === "") - (b === "") || newest(y) - newest(x) || a.localeCompare(b))
  .map(([title, list]) => [title, list.sort((a, b) => Number(b.inService ?? 0) - Number(a.inService ?? 0) || a.name.localeCompare(b.name))]);

// YAML by hand, so the checks can be comments beside what they are about.
const plain = /^[A-Za-z0-9][\w .,'’()&/-]*$/;
const q = (s) => (plain.test(s) && !/^(yes|no|true|false|null|on|off|~)$/i.test(s) && !/^[\d.:-]+$/.test(s) ? s : JSON.stringify(s));
const lines = [
  `title: ${q(args.title)}`,
  `summary: ${q(args.summary ?? (await describe(args.title, [...ships.values()].map((s) => ({ name: s.name, year: Number(s.inService) || undefined })))))}`,
  "source:",
  `  Wikidata: https://www.wikidata.org/wiki/${qid}`,
];
const flat = ordered.length === 1 && ordered[0][0] === "";
lines.push(flat ? "vessels:" : "sections:");
for (const [title, list] of ordered) {
  const indent = flat ? "  " : "      ";
  if (!flat) lines.push(`  - title: ${q(title || "Other ships")}`, "    vessels:");
  for (const s of list) {
    for (const c of s.checks) lines.push(`${indent}# check: ${c}`);
    lines.push(`${indent}- name: ${q(s.name)}`);
    if (s.mmsi) lines.push(`${indent}  mmsi: ${s.mmsi}`);
    if (s.imo) lines.push(`${indent}  imo: ${s.imo}`);
    const subtitle = [s.length && `${s.length} m`, s.inService && `in service ${s.inService}`].filter(Boolean).join(" · ");
    if (subtitle) lines.push(`${indent}  subtitle: ${q(subtitle)}`);
  }
}
const yaml = lines.join("\n") + "\n";

const all = [...ships.values()];
console.error(
  `${all.filter((s) => s.mmsi).length} with an MMSI the network confirms, ${all.filter((s) => !s.mmsi).length} without, ${all.filter((s) => s.checks.length).length} flagged to check`,
);
if (args.out) writeFileSync(args.out, yaml);
else process.stdout.write(yaml);
