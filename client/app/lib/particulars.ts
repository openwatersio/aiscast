import type { VesselUSCG, VesselWikidata } from "./api";

/** A label and its value, as Facts lists them; a fact without a value is left out. */
export type Fact = [label: string, value: string | undefined];

const meters = (n: number | undefined) => (n ? `${n.toLocaleString("en-US", { maximumFractionDigits: 2 })} m` : undefined);
const count = (n: number | undefined) => (n ? n.toLocaleString("en-US") : undefined);

/** Wikidata labels its classes in lower case, "cruise ship"; a value reads as "Cruise ship". */
const capitalized = (s: string | undefined) => (s ? s.charAt(0).toUpperCase() + s.slice(1) : undefined);

/** The particulars a vessel's Wikidata item holds. */
export function wikidataFacts(w: VesselWikidata): Fact[] {
  return [
    ["Type", capitalized(w.ship_type)],
    // P729 is when the vessel entered service, which is usually, not always, the year it was built.
    ["In service", w.year_built?.toString()],
    ["Builder", w.builder],
    ["Yard number", w.yard_number],
    ["Gross tonnage", count(w.gross_tonnage)],
    ["Deadweight", w.deadweight ? `${count(w.deadweight)} t` : undefined],
    ["Length", meters(w.length)],
    ["Beam", meters(w.beam)],
    ["Design draught", meters(w.draught)],
    ["Registry", w.registry],
    ["Home port", w.home_port],
    ["Owner", w.owner],
    ["Operator", w.operator],
    ["Former names", w.former_names?.join(", ")],
  ];
}

const nameKey = (s: string | undefined) => (s ?? "").toUpperCase().replace(/[^A-Z0-9]/g, "");

/**
 * Tonnage as the Coast Guard measured it. Convention tonnage is the international measure most
 * readers know; the older Regulatory system's register tons and the Simplified measure for small
 * vessels are different numbers, so they say which they are.
 */
function tonnage(n: number | undefined, measure: VesselUSCG["tonnage_measure"]) {
  const value = count(n);
  return value && measure && measure !== "Convention" ? `${value} (${measure.toLowerCase()})` : value;
}

/**
 * The particulars the Coast Guard documents for a US-flag vessel. The documented name is shown
 * only when it differs from the one AIS reports, which is often abbreviated: GOV THOMAS H KEAN is
 * documented as GOVERNOR THOMAS H. KEAN.
 */
export function uscgFacts(u: VesselUSCG, aisName?: string): Fact[] {
  const official = u.identification && /^\d+$/.test(u.identification);
  return [
    ["Documented as", nameKey(u.name) === nameKey(aisName) ? undefined : u.name],
    [official ? "Official number" : "Registration", u.identification],
    ["Service", u.service],
    ["Status", u.status],
    ["Built", u.year_built?.toString()],
    ["Length", meters(u.length)],
    ["Breadth", meters(u.beam)],
    ["Depth", meters(u.depth)],
    ["Gross tonnage", tonnage(u.gross_tonnage, u.tonnage_measure)],
    ["Net tonnage", tonnage(u.net_tonnage, u.tonnage_measure)],
  ];
}
