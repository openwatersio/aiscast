# Datalastic's MCP server

Compiled 2026-09-17 from the README of [datalastic/mcp-server-datalastic](https://github.com/datalastic/mcp-server-datalastic) and [datalastic.com/mcp](https://datalastic.com/mcp/). The point is to see the whole surface an AI assistant gets from the incumbent, and mark what aiscast has, can add cheaply, or should leave alone. The plan that acts on this is `specs/mcp.md`.

## How it is offered

- Remote only, at `https://mcp.datalastic.com/mcp`, Streamable HTTP. No local install.
- Two ways in: OAuth sign-in with a Datalastic account (the client opens a browser), or an `X-Api-Key` header for clients that support header config. Claude Desktop takes only the OAuth path.
- Client docs cover Claude Code, Cursor, VS Code with Copilot, Windsurf, Gemini CLI, and a generic "any Streamable HTTP client" block. ChatGPT connects through developer mode.
- Plans from €199 a month. Each successful call spends a credit, errors and not-found are free, and an overuse block stops requests when the month's credits run out.
- All 25 tools are listed to every client. The intelligence tools answer with an upsell message unless the Maritime Reports add-on is on the account, and an upgrade takes effect in the same session.
- Listed on LobeHub, Glama, PulseMCP, mcpservers.org, and mcp.so. Two community servers wrap the same REST API (`robderstadt/datalastic-mcp`, `pipeworx-io/mcp-datalastic`).

## The 25 tools

The verdict column says what aiscast should do. "Now" means the first release in `specs/mcp.md`. "Phase 2" needs the vessel cache to keep more of the type 5 static message. "Phase 3" needs `/v1/history`. "No" means the data is not in AIS and aiscast should not source it.

### Vessel tracking

| Tool | What it does | aiscast today | Verdict |
| --- | --- | --- | --- |
| `get_vessel` | Live position, speed, heading, navigation status for one vessel by MMSI or IMO | Everything except IMO lookup is in the vessel cache | Now as `get_vessels`. IMO lookup in phase 2 |
| `get_vessel_pro` | `get_vessel` plus recognised destination port, ETA, actual departure time, draught | Destination, ETA, and draught arrive in type 5 and are dropped at ingest | Phase 2 for destination, ETA, draught. Departure time needs history, so phase 3 at the earliest |
| `get_vessel_info` | Static specs: dimensions, tonnage, cargo capacity, year built, flag, call sign | Dimensions and call sign are in type 5. Flag is derivable from the MMSI's MID prefix with a static table | Phase 2 for dimensions, call sign, IMO, flag. Tonnage, capacity, and year built are registry data, not AIS. No |
| `find_vessels` | Search the registry by name, type, flag, tonnage, or dimensions | Names and type codes are cached | Now for name and type. Flag and dimensions in phase 2. Tonnage no |
| `get_vessels_bulk` | Live positions for up to 100 vessels in one call | `/v1/vessels?mmsi=` does this, capped by tier at 10, 50, or 200 | Now, as the list form of `get_vessels` |
| `get_vessel_history` | Historical track for a vessel, data since 2021-08-10 | The reception archive in R2 starts 2026-08 and has no query API | Phase 3 with `/v1/history` |
| `get_vessels_in_radius` | Every vessel within a radius, max 50 NM, around a point, a port, or a vessel | Bbox filtering plus the `nm()` helper cover point and vessel centres | Now for a point and for a vessel centre. Port centre needs a port table, see below |

### Ports

| Tool | What it does | aiscast today | Verdict |
| --- | --- | --- | --- |
| `find_ports` | Search a port registry by name, UN/LOCODE, country, type, or coordinates | Nothing. Type 5 destination strings are free text and often carry a UN/LOCODE | Maybe later. UN/LOCODE is open data and a small table would let radius search and destination fields resolve to a port name. Not in the first three phases |
| `get_port` | Terminals, operators, addresses, coordinates for a port | Nothing | No. Terminal and operator data is a commercial dataset |

### Weather

| Tool | What it does | aiscast today | Verdict |
| --- | --- | --- | --- |
| `get_weather` | Marine conditions merged with atmospheric weather at a point, a port, or around a vessel, with 7-day forecasts | Nothing in aiscast. Open Waters has tides at api.openwaters.io and could add sea state from open models | Not for aiscast. Worth noting for an Open Waters MCP that bundles tides beside vessels |

### Maritime intelligence

Twelve tools. All but `intel_info` need the paid Maritime Reports add-on on Datalastic. None of it is in AIS.

| Tool | What it does | Verdict |
| --- | --- | --- |
| `intel_ownership` | Beneficial owner, operator, technical and commercial manager, P&I club | No |
| `intel_inspections` | Port State Control inspection records, detentions, deficiencies | No. Some PSC regimes publish this openly, but it is a different product |
| `intel_casualties` | Groundings, collisions, fires, machinery failures | No |
| `intel_drydock` | Next dry dock, special survey, IOPP certificate expiry | No |
| `intel_class` | Classification society, principal dimensions, next survey dates | No |
| `intel_engine` | Main engine model, builder, propulsion, maximum continuous output | No |
| `intel_spd` | Sale and purchase and demolition transactions | No |
| `intel_companies` | Maritime company profiles and contacts | No |
| `sea_route` | Schematic sea route and distance between two ports or coordinates | No. Great-circle distance between two points is trivial and could ride inside `find_vessels_near` output, but routing is out of scope |
| `estimated_vessel_position` | Estimated current position for vessels out of terrestrial AIS range | Maybe. Dead reckoning from last position, SOG, and COG is a few lines. Useful because aiscast drops vessels after 30 minutes unseen. Consider an `estimated` position on rows older than a few minutes |
| `intel_report_request` | Bulk export of any Maritime Reports dataset, async | No |
| `intel_info` | Add-on status and how to enable it | Not applicable. aiscast has no add-ons |

### Bulk reports

| Tool | What it does | aiscast today | Verdict |
| --- | --- | --- | --- |
| `report_request` | Async export of a vessel list, port list, area history, or usage | Nothing | Phase 3 at the earliest, and only if `/v1/history` needs an async form for large ranges. The R2 archive may end up public instead, which makes this moot |
| `report_status` | Poll a report job, get a signed download URL | Nothing | Same |
| `report_list` | Recent report jobs | Nothing | Same |

## What this says about the first release

- Five of the seven vessel-tracking tools are answerable from the cache today. That is the whole live-position story an assistant needs.
- Type 5 fields are the cheapest big win. Destination, ETA, draught, dimensions, call sign, IMO, and a flag from the MID table turn `get_vessels` into `get_vessel_pro` and most of `get_vessel_info` for about 60 bytes per vessel.
- A vessel-centred radius search costs nothing extra and matches a real question ("what is near the ferry right now").
- A UN/LOCODE table is the one non-AIS dataset worth considering, because it makes destinations and radius centres human. Open data, one file, no licensing question.
- History is the only Datalastic tool that people pay for and aiscast cannot yet answer. It is already on the roadmap for other reasons.
- The intelligence and weather tools are what the €199 buys beyond positions. aiscast does not compete there and should say so in its server instructions, so an assistant does not try.

## Example prompts they publish

Useful as test conversations for aiscast's own server, minus the ones that need registry or intelligence data:

- Where is the MSC Oscar right now and what is its destination?
- Show me all tankers within 20 nautical miles of Rotterdam.
- What are the wave and wind conditions around the Ever Given right now? (weather, not ours)
- Who is the beneficial owner of IMO 9839179? (intelligence, not ours)
- Find all bulk carriers built after 2018 flagged in the Marshall Islands. (year built is registry data; type and flag are ours in phase 2)
- Which vessels does Maersk operate? (not ours)
