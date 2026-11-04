#!/usr/bin/env node
import { readFileSync, writeFileSync } from "node:fs";
import { resolve } from "node:path";
import { pathToFileURL } from "node:url";
import { parseArgs } from "node:util";

const SOURCES = {
  "bc-ferries": "https://www.bcferries.com/on-the-ferry/our-fleet",
  "black-ball": "https://www.cohoferry.com/about-us",
  "hy-line": "https://www.hylinecruises.com/fleet/",
  // WSDOT's vessel feed, which answers without an access code; its "About our fleet" page names no vessels.
  wsf: "https://www.wsdot.wa.gov/ferries/api/vessels/rest/vesselbasics",
};
const text = (html) => html.replace(/<[^>]*>/g, " ").replace(/&(#x[0-9a-f]+|#\d+|amp|apos|quot|nbsp|rsquo|lsquo);/gi, (_, entity) => {
  if (entity.startsWith("#")) return String.fromCodePoint(entity[1].toLowerCase() === "x" ? parseInt(entity.slice(2), 16) : Number(entity.slice(1)));
  return { amp: "&", apos: "'", quot: '"', nbsp: " ", rsquo: "’", lsquo: "‘" }[entity.toLowerCase()];
}).replace(/\s+/g, " ").trim();
const nameKey = (name) => name?.trim().toUpperCase();

export function parseRoster(operator, html) {
  const source = SOURCES[operator];
  if (!source) throw new Error(`Unknown operator: ${operator}`);
  let vessels;
  if (operator === "bc-ferries") {
    // The cards are paginated; the selector contains the complete roster.
    const selector = /<select\b[^>]*\bid=["']shipCodeFilter["'][^>]*>([\s\S]*?)<\/select>/i.exec(html)?.[1];
    if (!selector) throw new Error("BC Ferries roster selector not found");
    vessels = [...selector.matchAll(/<option\b[^>]*\bvalue=["']([^"']+)["'][^>]*>([\s\S]*?)<\/option>/gi)]
      .filter(([, value]) => value !== "ALL")
      .map(([, value, label]) => {
        if (!/^[a-z0-9-]+\/[A-Z0-9_]+$/.test(value)) throw new Error(`Unexpected BC Ferries vessel path: ${value}`);
        return { name: text(label), links: { "Official vessel": `${source}/${value}` } };
      });
  } else if (operator === "hy-line") {
    vessels = [];
    for (const block of html.split(/<div\b[^>]*class=["']block-accordion-item(?:\s[^"']*)?["'][^>]*>/i).slice(1)) {
      const name = text(/<button\b[^>]*>([\s\S]*?)<\/button>/i.exec(block)?.[1] ?? "");
      const description = text(/<p\b[^>]*>([\s\S]*?)<\/p>/i.exec(block)?.[1] ?? "");
      if (!name || !description) throw new Error("Hy-Line roster item is missing its name or description");
      if (/\b(?:operates|operating|service)\b[^.]*\bbetween\b/i.test(description)) vessels.push({ name });
      else if (!/fishing trips|Harbor Cruise|Canal Cruises/i.test(description)) throw new Error(`Check Hy-Line service for ${name}: ${description}`);
    }
  } else if (operator === "wsf") {
    let feed;
    try { feed = JSON.parse(html); } catch { throw new Error("WSDOT vessel feed is not JSON"); }
    if (!Array.isArray(feed) || feed.some((v) => typeof v?.VesselName !== "string" || !Number.isInteger(v.VesselID) || typeof v.OwnedByWSF !== "boolean"))
      throw new Error("WSDOT vessel feed has an unexpected shape");
    // Status (in service, maintenance, out of service) is not membership; WSDOT drops a sold hull from the feed. Leased hulls are not the fleet.
    vessels = feed.filter((v) => v.OwnedByWSF).map((v) => ({ name: text(v.VesselName), links: { "Official vessel": `https://wsdot.com/ferries/vesselwatch/VesselDetail.aspx?vessel_id=${v.VesselID}` } }));
  } else {
    if (!/<h2\b[^>]*>\s*The M\.V\. Coho\s*<\/h2>/i.test(html) || !/Black Ball Ferry Line operates the M\.V\. Coho/i.test(text(html)))
      throw new Error("Black Ball's current Coho roster description not found");
    vessels = [{ name: "Coho" }];
  }
  if (!vessels.length) throw new Error(`${operator}: empty roster`);
  if (vessels.some((v) => !v.name) || new Set(vessels.map((v) => nameKey(v.name))).size !== vessels.length)
    throw new Error(`${operator}: missing or duplicate vessel names`);
  return vessels;
}

export async function refreshRoster(fleet, official, api) {
  const changes = [];
  const retained = new Set();
  const vessels = [];
  for (const member of official) {
    const previous = fleet.vessels.find((v) => (member.links?.["Official vessel"] && v.links?.["Official vessel"] === member.links["Official vessel"]) || nameKey(v.name) === nameKey(member.name));
    if (previous) retained.add(previous);
    else changes.push(`Added: ${member.name}`);
    if (previous && previous.name !== member.name) changes.push(`Name: ${previous.name} → ${member.name}`);
    const vessel = { ...previous, ...member, links: { ...previous?.links, ...member.links } };
    const matchesName = (p) => p?.name && [member.name, previous?.ais_name].some((name) => name && nameKey(name) === nameKey(p.name));
    const matches = (p) => (!p?.kind || p.kind === "vessel") && matchesName(p) && !(previous?.imo && p.imo && previous.imo !== p.imo);
    const record = previous?.mmsi ? await api(`/v1/vessels/${previous.mmsi}`) : undefined;
    if (record && (!record.properties || !/^[2-7]\d{8}$/.test(String(record.properties.mmsi)))) throw new Error(`Invalid AIS record for ${member.name}`);
    let heard = record?.properties;
    let candidates = [];
    if (!heard || !matches(heard)) {
      const queries = new Set([member.name, previous?.ais_name].filter(Boolean));
      for (const query of queries) {
        const search = await api(`/v1/vessels?q=${encodeURIComponent(query)}`);
        if (search && (!Array.isArray(search.features) || search.features.some((f) => !f.properties || !/^[2-7]\d{8}$/.test(String(f.properties.mmsi)))))
          throw new Error(`Invalid AIS search for ${member.name}`);
        for (const f of search?.features ?? []) if (!candidates.some((p) => p.mmsi === f.properties.mmsi)) candidates.push(f.properties);
      }
      const confirmed = candidates.filter((p) => previous?.imo && p.imo === previous.imo && matches(p));
      heard = confirmed.length === 1 ? confirmed[0] : undefined;
    }
    if (heard && matches(heard)) {
      vessel.mmsi = heard.mmsi;
      vessel.imo = heard.imo || previous?.imo;
      vessel.ais_name = heard.name;
      vessel.links["AIS identity"] = `https://ais.openwaters.io/v1/vessels/${heard.mmsi}`;
      if (previous?.mmsi !== vessel.mmsi) changes.push(`MMSI: ${member.name}: ${previous?.mmsi ?? "none"} → ${vessel.mmsi}`);
      if (previous?.imo !== vessel.imo) changes.push(`IMO: ${member.name}: ${previous?.imo ?? "none"} → ${vessel.imo}`);
      if (previous?.ais_name !== vessel.ais_name) changes.push(`AIS name: ${member.name}: ${previous?.ais_name ?? "none"} → ${vessel.ais_name}`);
    } else {
      delete vessel.mmsi;
      delete vessel.ais_name;
      delete vessel.links["AIS identity"];
      changes.push(`Unresolved: ${member.name}${previous?.mmsi ? ` (was ${previous.mmsi})` : ""}. Verify candidates before adding an identity: ${candidates.map((p) => `${p.name} MMSI ${p.mmsi} IMO ${p.imo ?? "unknown"}`).join("; ") || "none"}`);
    }
    if (!Object.keys(vessel.links).length) delete vessel.links;
    vessels.push(vessel);
  }
  for (const v of fleet.vessels) if (!retained.has(v)) changes.push(`Removed: ${v.name}; verify sale, retirement or rename`);
  return { draft: { ...fleet, vessels }, changes };
}

async function main() {
  const { values: args } = parseArgs({ options: { operator: { type: "string" }, out: { type: "string" } } });
  if (!SOURCES[args.operator]) throw new Error(`usage: refresh-ferry-fleet.mjs --operator ${Object.keys(SOURCES).join("|")} [--out /tmp/draft.json]`);
  const input = new URL(`../app/fleets/ferries/${args.operator}.json`, import.meta.url);
  const fleet = JSON.parse(readFileSync(input, "utf8"));
  const request = async (url) => {
    const response = await fetch(url, { signal: AbortSignal.timeout(30_000), headers: { "user-agent": "aiscast-ferry-rosters/1.0 (https://openwaters.io/ais/)" } });
    if (!response.ok && response.status !== 404) throw new Error(`${url}: HTTP ${response.status}`);
    return response;
  };
  const html = await request(SOURCES[args.operator]);
  if (!html.ok) throw new Error(`Official roster: HTTP ${html.status}`);
  const official = parseRoster(args.operator, await html.text());
  const { draft, changes } = await refreshRoster(fleet, official, async (path) => {
    await new Promise((done) => setTimeout(done, 650));
    const response = await request(`https://ais.openwaters.io${path}`);
    return response.ok ? response.json() : undefined;
  });
  console.error(`${official.length} official members. Review every change, verify new identities, and update reviewed after approval.\n${changes.join("\n") || "No membership or identity changes."}`);
  const json = JSON.stringify(draft, null, 2) + "\n";
  // Exclusive creation protects both maintained files and earlier drafts, including symlinks.
  if (args.out) writeFileSync(resolve(args.out), json, { flag: "wx" });
  else process.stdout.write(json);
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href)
  main().catch((error) => { console.error(error.message); process.exitCode = 1; });
