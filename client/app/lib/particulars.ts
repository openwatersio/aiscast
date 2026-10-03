import type { VesselParticulars } from "./api";

/** A label and its value, as Facts lists them; a fact without a value is left out. */
export type Fact = [label: string, value: string | undefined];

const meters = (n: number | undefined) => (n ? `${n.toLocaleString("en-US", { maximumFractionDigits: 2 })} m` : undefined);
const count = (n: number | undefined) => (n ? n.toLocaleString("en-US") : undefined);

/** Wikidata labels its classes in lower case, "cruise ship"; a value reads as "Cruise ship". */
const capitalized = (s: string | undefined) => (s ? s.charAt(0).toUpperCase() + s.slice(1) : undefined);

/** Case-insensitive exact comparison: punctuation and accents count, since AIS cannot carry them. */
const sameName = (a: string | undefined, b: string | undefined) =>
  (a ?? "").trim().toUpperCase() === (b ?? "").trim().toUpperCase();

/**
 * Tonnage as the flag state measured it. Convention tonnage is the international measure most
 * readers know; the older Regulatory system's register tons and the Simplified measure for small
 * vessels are different numbers, so they say which they are.
 */
function tonnage(n: number | undefined, measure: VesselParticulars["tonnage_measure"]) {
  const value = count(n);
  return value && measure && measure !== "Convention" ? `${value} (${measure.toLowerCase()})` : value;
}

/**
 * The merged particulars, one list. The documented name is shown unless it matches the AIS name
 * exactly, case aside: AIS cannot carry an apostrophe or an accent, so RUBYS STAR documented as
 * RUBY'S STAR is worth a row, and GOV THOMAS H KEAN documented as GOVERNOR THOMAS H. KEAN more so.
 * `tonnage_measure` describes the flag state's figures, so it annotates a tonnage only when
 * `provenance` says the flag state supplied it.
 */
export function particularsFacts(m: VesselParticulars, aisName?: string, provenance?: Record<string, string>): Fact[] {
  const official = m.identification && /^\d+$/.test(m.identification);
  const measure = (field: string) => (!provenance || provenance[field] === "uscg" ? m.tonnage_measure : undefined);
  return [
    ["Documented as", sameName(m.registered_name, aisName) ? undefined : m.registered_name],
    [official ? "Official number" : "Registration", m.identification],
    ["Type", capitalized(m.ship_type)],
    ["Service", m.service],
    ["Status", m.status],
    ["Built", m.year_built?.toString()],
    ["Builder", m.builder],
    ["Yard number", m.yard_number],
    ["Gross tonnage", tonnage(m.gross_tonnage, measure("gross_tonnage"))],
    ["Net tonnage", tonnage(m.net_tonnage, measure("net_tonnage"))],
    ["Deadweight", m.deadweight ? `${count(m.deadweight)} t` : undefined],
    ["Length", meters(m.length)],
    ["Beam", meters(m.beam)],
    ["Depth", meters(m.depth)],
    ["Design draught", meters(m.draught)],
    ["Registry", m.registry],
    ["Home port", m.home_port],
    ["Owner", m.owner],
    ["Operator", m.operator],
    ["Former names", m.former_names?.join(", ")],
  ];
}
