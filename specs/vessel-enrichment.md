# Vessel enrichment shape

`GET /v1/vessels/{mmsi}` serves particulars from two synced sources, Wikidata and the Coast Guard's PSIX, each as its own object: `properties.wikidata` and `properties.uscg`, each with its own field names, its own license field, and its own client rendering. Every new source (FCC ULS, Fiskeridirektoratet, the EU fleet register, Transport Canada, AMSA) would add another key, another vocabulary, and another client section. This spec replaces the per-source keys with one canonical shape. Detail endpoint only; area results, search, tiles, and the stream stay as they are, because a shape can be added later but not taken away.

## Shape

On `/v1/vessels/{mmsi}` and the MCP `get_vessels` tool, three properties replace `wikidata` and `uscg`:

```json
"particulars": {
  "ship_type": "heavy lift ship",
  "year_built": 2011,
  "builder": "Larsen & Toubro",
  "gross_tonnage": 14784,
  "length": 156.93, "beam": 25.6,
  "registry": "Netherlands", "home_port": "Amsterdam",
  "wikipedia": "https://en.wikipedia.org/wiki/...",
  "image": "https://commons.wikimedia.org/wiki/File:..."
},
"provenance": { "ship_type": "wikidata", "year_built": "uscg", "length": "uscg", "...": "..." },
"sources": {
  "wikidata": { "credit": "Wikidata", "license": "CC0-1.0", "url": "https://www.wikidata.org/wiki/Q83638155" },
  "uscg": { "credit": "U.S. Coast Guard PSIX", "license": "public domain (U.S. government work)", "url": "https://cgmix.uscg.mil/PSIX/..." }
}
```

- `particulars` is the merged document: one vocabulary, one unit system (metres, tonnes), no source names in field names. Consumers who do not care about provenance read this and nothing else.
- `provenance` says which source supplied each present field. Its values are keys of `sources`.
- `sources` is the structured attribution for the record: one entry per source that contributed, with a display credit, the license, and a URL to the source's own page for this vessel, so a reader can check the claim. The top-level `attribution` map keeps its existing meaning, credit lines for the position feed, and is not touched; mixing object values into that string map would break every client that renders it.

The field vocabulary is the union of what the sources supply, each concept once:

| Field | Sources | On conflict |
| --- | --- | --- |
| `registered_name` | uscg | |
| `identification` | uscg (official number or state registration) | |
| `service`, `status`, `tonnage_measure` | uscg | |
| `ship_type` | wikidata | |
| `builder`, `yard_number`, `deadweight`, `draught`, `home_port`, `owner`, `operator`, `former_names` | wikidata | |
| `wikipedia`, `commons_category`, `image` | wikidata | |
| `year_built`, `length`, `beam` | both | registry wins |
| `gross_tonnage` | both | registry wins when measured by the Convention system; a Regulatory or Simplified figure is not the number readers expect, so Wikidata wins over those, and `tonnage_measure` always says how the served figure was measured when it came from PSIX |
| `net_tonnage`, `depth` | uscg | |
| `registry` | both | a flag-state match is itself the registry fact, so it wins |

Merge rules are per-field and deterministic: a flag-state registry outranks Wikidata for registered facts, empty values never win, and `provenance` records the winner. The raw stores stay as they are; the `wikidata` and `uscg` tables already keep each source's records verbatim, so the merge is a serving-time function and a policy change re-runs it for free.

A future source slots in by adding fields to the vocabulary, a rank to the precedence, and an entry to `sources`. No response key is named after it except in `provenance` and `sources`.

Identifiers stay where they are: `imo`, `callsign`, and `mmsi` are already top-level properties. A dedicated `identifiers` object earns its place when a source introduces key types AIS does not carry, such as a CFR or a national official number used for joining; `identification` covers the one such value served today.

## Client

The vessel page renders one Particulars section from `particulars`, in place of the two per-source sections, with the same formatting rules it has now (Convention tonnage plain, other measures labelled; the documented name only when it differs from the AIS name). A footer line under the facts names the sources from `sources`, each linking its `url`: "Source: Wikidata · U.S. Coast Guard". CC0 and public domain require no credit; the line is provenance for the reader, not a license obligation.

## Work

- [ ] `server/particulars.go`: the canonical struct, the merge, the `sources` builder; unit test over the conflict rules.
- [ ] `server/vessels.go` and `server/mcp.go`: serve the new properties in place of `wikidata` and `uscg`.
- [ ] `server/openapi.json` and `server/README.md`: document the shape.
- [ ] `client`: `api.ts` types, `particulars.ts` reduced to formatting, `VesselDetail.tsx` one section with the source footer.
