# Station names and heard-first counts

Plan for [#51](https://github.com/openwatersio/aiscast/issues/51) (let a station name itself) and [#53](https://github.com/openwatersio/aiscast/issues/53) (name and heard-first count in `/v1/stations`).

## Decisions

1. **Heard-first is unique vessels: those no other station heard in the last 24 hours.** New fields `vessels_24h` and `vessels_exclusive_24h` on every `/v1/stations` row. It is computed in `stations.go` with the same set logic `/v1/stats` uses for `vessels_exclusive`, keyed by station instead of by source kind. This ships first because it needs no new state.
2. **Every volunteer station gets a coverage label, `near`.** It names the place nearest the median position of the vessels the station heard in the last 24 hours: the largest town of at least 5,000 people within 10 km, otherwise the nearest such town within 25 km, otherwise the region, such as "Munster, Ireland". Place names come from GeoNames `cities5000`, embedded in the server. A station with no other name shows as "Near Santa Monica, CA". This labels every receiving station at once with nothing for operators to do. It replaces a typed place label, so there is no free-text location to moderate.
3. **A station that sends `!AIVDO` is named after its own vessel by default.** The own MMSI comes from `!AIVDO` sentences, for token stations as well as UDP stations. The name comes from the vessel record. If two different MMSIs arrive within an hour, the station gets no automatic name. An operator-set name always wins.
4. **Operators name the station when they generate its token.** `POST /v1/keys` takes an optional `name`. A request with a name must be signed by the device key. The server stores the name by station, not in the token, so renaming means generating again, and the token already in the receiver's config keeps working. The token page gets a name field beside the generate button. The signature is needed because `/v1/keys` does not check possession of the key today, and the key is in the public station id, so anyone could otherwise name any station.
5. **UDP stations are named through a bound address.** A mint with both `name` and `bind_ip` also names the UDP station of the requester's address. UDP stations that send `!AIVDO` get the vessel name without doing anything.
6. **No AIS-catcher field and no URL parameter.** Both are bearer-only, so they have the same hijack hole as decision 4. They also cover only some transports.
7. **No email.** Names need no contact address. Moderation is reactive: a `LOCKED_STATIONS` list on the box clears and locks a name. Operators are reached through the public station page, the plugin status line, and the monthly digest. Revisit when operators ask for outage alerts, or when there are about 100 named stations.
8. **The Signal K plugin names its station after the boat, with no new setting.** It sends the vessel name configured in Signal K as `vessel_name` on its signed mint, but only while "Share my own ship" is on. That switch is how an operator keeps the boat's whereabouts private, and a boat's name beside its coverage label would give them away.
9. **Station state lives in a `stations` table in the SQLite vessel record**, `aiscast.db`. It holds names, own MMSIs, the last coverage label, and the last signed mint time per station. The file already survives deploys, and nothing else about stations is stored today.
10. **Proof of possession happens once, at mint.** Every mint may be signed by its key. A key that has signed once must always sign, so each operator is protected from their first signed mint. The token page and the plugin sign every mint. Unsigned mints are refused once a counter shows nobody still sends them. Tokens stay bearer tokens, because AIS-catcher cannot sign.

A station's display name is the first of these that exists: the operator name, the vessel name the plugin sent, the own vessel's name from `!AIVDO`, "Near" plus the coverage label, then the id.

Phases, smallest first: heard-first count, then coverage labels, then own-vessel names, then operator names on the token page, then the Signal K plugin sending its vessel name, then refusing unsigned mints.

## How stations are identified

| Transport | Station id | Who holds the key | Own vessel |
| --- | --- | --- | --- |
| UDP: AIS-catcher `-u`, docker-shipfeeder, `ais-forwarder` from Signal K | `udp:<keyed hash of address>` | Nobody | `!AIVDO` re-keys the station to `mmsi:<n>` ([server/pipeline.go](../server/pipeline.go), `ownOf`) |
| MQTT, `POST /v1/receive`, `/v1/stream` publish | `station:<sub>`, where a personal token's sub is `ed25519:<pubkey>` | The browser that used the token page (`localStorage`), or the Signal K plugin (`identity.json`) | Not detected |
| Any of the above with a TAG `s:` field | `<id>/<tag>`, for example `/n2k` or `/self` from the Signal K plugin | As above | As above |

On 2026-09-30, `/v1/stations` listed 19 volunteer rows: 13 `station:`, 5 `udp:`, and 1 `mmsi:`. One `station:` row exists only as `/self`. It is a Signal K boat with no receiver that shares its own position built from GPS.

The Signal K plugin sends the boat's own ship as `!AIVDO`. It does this for a live transponder, which it matches by the boat's MMSI on NMEA 2000 and tags `s:n2k`. It also does this for self-reports built from Signal K, which it tags `s:self`. Both are behind the plugin's own-ship switches. A UDP `ais-forwarder` sends `!AIVDO` when the operator forwards it.

## Heard-first count (#53)

**What counts.** A vessel counts for a station when the station delivered an accepted event or a duplicate for that MMSI. This is the same rule `vessels` uses today, so a station that is always beaten to a message by a faster feed still gets credit for hearing the vessel. A vessel is exclusive to a station when no other station heard it in the window. "Other station" means every other row in `/v1/stations`, including feeds and aggregates, because the question is whether anyone else in the network hears it. Rows that share a base id (the part before `/`) count as one station, so `/n2k` and `/self` rows do not compete with their own base row.

**What does not count.**

- The station's own vessel. Own-ship `!AIVDO` is not reception, and a boat in a remote anchorage would otherwise always add one exclusive vessel. The event gets an `Own` flag in `ingestLine`, set when the sentence is `VDO`.
- Stale events. They never reach `stationStats.event` today. This matters for AISHub: the server feeds volunteer receptions to AISHub and polls AISHub back, so a volunteer's vessel can return as an AISHub row. That row is older than the volunteer's own copy, so it is stale and does not take the vessel's exclusivity away. A test must pin this, including the case where the AISHub row has the same second as the original.

**Window: 24 hours.** A 30-minute snapshot changes with the time of day and with ferry schedules, so it is a poor number for a leaderboard that is read once a month. 24 hours matches `events.last_24h`. 7 days would need about twice the memory, because the network hears about 177,000 vessels in 7 days and about 96,000 in 24 hours. That window can come later if the digest wants it.

**Where it is computed.** In [server/stations.go](../server/stations.go):

- `sweep` keeps each station's vessel map for 24 hours instead of `vesselTTL`. `vessels` keeps its 30-minute meaning by counting only entries newer than `vesselTTL`. `vesselsBySource` filters the same way, so `/v1/stats` does not change.
- A new `exclusive(now)` runs the `vesselsBySource` loop keyed by base station id. It skips each station's own MMSIs, recorded from its `VDO` sentences. `vessels` still counts them. The result is cached for 60 seconds, because the loop touches every entry and `/v1/stations` is public.
- `rows` adds `vessels_24h` and `vessels_exclusive_24h`.

Sizing: on 2026-09-30, per-source sets summed to about 72,000 vessels in 30 minutes, with AISHub at 44,000 and aisstream at 23,000. 24-hour sets should be roughly double that, about 150,000 map entries or 10 MB. `vessels_bench_test.go` should measure it before this merges.

The 24-hour maps are written at shutdown, beside the usage file, and restored at start. A deploy is a clean shutdown, so a deploy loses nothing. A crash rebuilds the sets over 24 hours. Writing them every minute with the usage file would be about 3 MB a minute for no gain.

**Name in the API.** The fields are `vessels_24h` and `vessels_exclusive_24h`. The issue calls this "heard first", but the station page already uses "Heard first elsewhere" for `duplicates`, which is a message race. The client labels the new number "Unique vessels".

## Coverage labels

Every volunteer row (`udp:`, `station:`, `mmsi:`) gets a `near` field naming the place nearest the traffic it hears. Feed and aggregate rows get none. The field is present on named stations too, so a leaderboard can show "Quissett Harbor, near Falmouth, MA". `near` is a separate field rather than a value of `name`, so a script can tell a derived label from a chosen one.

**The point.** The input is the station's 24-hour vessel map from phase 1, not including its own vessel. Each entry in the map keeps the last position the station itself heard for that vessel, so the point describes this station's traffic and needs no lookups. The point is the median latitude and the median longitude of those positions. Medians keep one bad position, or a vessel that has since sailed far away, from moving the point. The station needs at least 5 vessels with positions. Below that it keeps its last label, so a quiet night or a deploy does not blank it.

The point describes the traffic, not the antenna. A receiver on a hill above a bay gets the town on the bay. That is the right label for coverage, and it means the label never pins down where the receiver sits.

**The place.** From GeoNames `cities5000`, without neighborhoods, historical places, and abandoned places (feature codes `PPLX`, `PPLH`, `PPLQ`, `PPLW`, `PPLCH`):

1. The most populous place within 10 km of the point. This prefers "Valencia" over its suburb "Sedaví" and "Victoria" over "Oak Bay".
2. Otherwise the nearest place within 25 km. This finds a small harbor town such as "Morehead City, NC" or "Belfast, ME" rather than a larger inland town.
3. Otherwise the first-level region of the nearest place, within 100 km: "Munster, Ireland" or "Split-Dalmatia, Croatia". Remote coasts have no town of 5,000 nearby, and naming one 60 km inland would mislead.
4. Beyond 100 km there is no label. The station hears open ocean, or its traffic is too spread out to name.

GeoNames is CC BY 4.0, so the credit "Place names: GeoNames" goes in the README's data credits and the API reference. Natural Earth's populated places are public domain, but there are about 7,300 of them and few are small coastal towns. Natural Earth would label a Santa Monica station "Los Angeles".

- A build script trims `cities5000` (about 64,000 places after the exclusions) to name, population, country, region code, latitude, and longitude, and adds the region and country names from `admin1CodesASCII.txt` and `countryInfo.txt`. The server embeds the result with `go:embed`, about 2 MB. The script lives with the server and runs by hand when the list needs a refresh.
- A linear scan per station is fast enough: 64,000 distance checks for each of about 30 stations, every 10 minutes.
- Format: US places are "Town, ST", because GeoNames region codes for the US are postal abbreviations. Everywhere else is "Town, Country", with the country spelled out so that "CA" never means both Canada and California. A US region is the state name alone ("Maine"). Elsewhere it is "Region, Country".

**Checked against live stations.** On 2026-09-30, 18 volunteer stations reported 5 or more vessels. The others were own-ship rows (`/self`) that will carry vessel names, or rows with no positions. For those 18, the rule above gave:

| Rule | Good labels | Problems |
| --- | --- | --- |
| Nearest town of 15,000+ | 14 | "Havelock, NC" 28 km inland for a Morehead City boat. "Scicli" inland of a coastal Sicilian station. "Tralee" 80 km and "Split" 52 km away for the Irish and Croatian stations. |
| Nearest town of 5,000+ | 15 | "Sedaví", a suburb, for a Valencia station. Ireland and Croatia still 50 to 60 km off. |
| The rule above | 18 | None. Ireland and Croatia get their regions. |

The full comparison, including the 15,000 and 5,000 lists' choices for every station, is a throwaway script. The numbers are worth rechecking when there are more stations.

**When it runs.** One minute after start, once stations have reported, then every 10 minutes. The last label per station is saved in the `stations` table.

**Two stations near one town.** Both get the same label. The client adds the end of the id where two display names match, for example "Near Santa Monica, CA (…c34f)".

**Moving stations.** A boat's label follows the boat. For a boat that also sends `!AIVDO`, its own position is already public. For a boat that shares targets but not its own position, the label shows the town it is near now. The station's vessel list and bbox already show the same thing in more detail.

**Privacy.** [docs/policy.md](../docs/policy.md#privacy) says derived locations are shown only coarse. A town of at least 5,000 people, or a region, placed by the traffic rather than the antenna, is coarser than the bbox `/v1/stations` already publishes. The policy line on station locations gains one sentence: "Each station is labeled with the town or region nearest the traffic it hears."

## Own-vessel names (#51, question 1)

**Detection.** Any `VDO` sentence from a station whose MMSI has a known MID (`flagOf` in [server/mid.go](../server/mid.go) returns a country) names that station's own vessel. The key is the base station id, so `station:ed25519:X/n2k` and `station:ed25519:X/self` both set the own vessel for `station:ed25519:X`. UDP stations keep today's behavior: the row becomes `mmsi:<n>` and its own vessel is `n`. `station:` rows are not re-keyed, because the token's sub is the authenticated identity. They get an entry in an `own` map instead. The map holds the MMSI, the last time it was seen, and the previous MMSI with its last time.

**Name.** The display name is the vessel record's name (`p.vessels[mmsi].Name`, trimmed of `@` padding). Any source's static data counts, so the name appears even when another receiver heard the boat's type 5 or type 24 message. Self-reports from the plugin carry the Signal K vessel name in their type 24 static message every 6 minutes.

**Cases.**

- **No `!AIVDO`.** There is no vessel name. The station shows its coverage label, or its id if it has none, until an operator names it.
- **`!AIVDO` but no name known yet.** The row carries `mmsi` without `name`. The client shows "MMSI 367430440" until static data arrives.
- **Several vessels.** If two different valid MMSIs arrive within one hour, the station gets no automatic name until one of them has been the only one for an hour. A forwarder that relays other boats' own-ship data is misconfigured, and a name that flips is worse than none. UDP re-keying keeps its last-wins rule. That rule changes the row id, not a name.
- **The vessel changes.** Examples are a sold boat, a plugin moved to another boat, or a reprogrammed transponder. The new MMSI takes over after an hour without the old one. A UDP station becomes a new `mmsi:<new>` row. A `station:` row keeps its id and takes the new vessel's name.
- **The vessel is renamed.** The station follows the static data.
- **An operator-set name exists.** It wins. The row still carries `mmsi`.

**In the API.** A row carries its own vessel's `mmsi` whenever it is known, on `station:` rows as well as `mmsi:` rows, so the station page can link to the vessel.

**Persistence.** Own MMSIs go in the `stations` table. A moored boat without a transponder sends nothing while its position is unchanged, so after a deploy its station would otherwise stay unnamed until the boat moves.

**Privacy.** [docs/policy.md](../docs/policy.md#privacy) already says a feed that includes `!AIVDO` publishes its own position and that the station is identified by that MMSI. Naming a `station:` row after its vessel applies the same rule to token stations. It does link the station's coverage bbox to a named boat, but the boat's own position is already public in the same feed. Consent follows the data: the plugin sends `!AIVDO` only with own-ship sharing on, and a forwarder sends it only when configured to. The policy line and the matching sentence in [docs/contributor-agreement.md](../docs/contributor-agreement.md) change from "identified by that MMSI" to "identified and named by that vessel". The policy's vessel opt-out does not exist in code yet. When it does, an opted-out own vessel gives no name.

## Operator-set names (#51, question 2)

### Why the request is signed

`POST /v1/keys` mints a personal token for any public key without proof of possession ([server/auth.go](../server/auth.go), `mintPersonal`). A station's public key is its id, and `/v1/stations` publishes the id. So anyone can mint a token whose sub is someone else's station. If an unsigned mint could set a name, anyone could rename any station. The name is public and lasts until changed, so a mint that names a station must prove the key.

### Naming at mint

`POST /v1/keys` and the `/v1/stream` register frame both go through `mintPersonal`, so both accept the same new fields:

```json
{"pubkey": "<base64url>", "bind_ip": true, "name": "Quissett Harbor", "ts": 1790000000, "sig": "<base64url>"}
```

- Without `name`, the mint works as it does today and needs no signature. Plugin versions already installed keep working.
- With `name` or `vessel_name`, `sig` must be an Ed25519 signature by `pubkey` over `aiscast-key\n<pubkey>\n<ts>\n<name>\n<vessel_name>`. Plain lines avoid JSON canonicalization.
- `name` is a name the operator chose. `vessel_name` is the boat's name as the sender knows it, and only the Signal K plugin sends it. The two differ in one rule: `vessel_name` does not have to be unique, because two boats can share a name.
- A name that breaks the rules never blocks the token. The server issues the token, stores no name, and says why in `name_error`. The token page shows the error. The plugin logs it and keeps sharing data.
- `ts` must be within 5 minutes of the server clock and later than the last accepted `ts` for the station. This stops replays without a challenge round trip.
- The server stores the name for `station:ed25519:<pubkey>`. With `bind_ip`, it also stores it for `udpStation(ip)` of the requester's address, which is the same station `contributed24h` credits for the feeder tier.
- An empty `name` or `vessel_name` with a valid signature clears it, and the next name in the display order returns.
- Moderation: `LOCKED_STATIONS`, a comma-separated list of station ids, sits beside `REVOKED_SUBS` and is read the same way. A locked station shows no operator or vessel name and refuses signed names. The station keeps publishing. Reports go to the published contact address.
- Limits: 10 names per station per day, and the existing `/v1/keys` per-address limiter.

### Why the name is not a token claim

The token could carry the name in its claims, but the server would still need the name stored by station:

- **Renaming would mean replacing the token in the receiver's config.** An AIS-catcher operator would edit the MQTT URL and restart. With the name stored by station, renaming is one click on the token page.
- **Old tokens never expire.** Two tokens for one station with different names would flip the name depending on which one published last.
- **UDP stations never present a token.** The server sees the token only at mint, so the name for a bound address has to be stored then anyway.
- **Moderation.** Clearing a name in a claim means revoking the token, which stops the station's data.

Leaving the name out of the claims keeps one source of truth.

### Name rules

- 1 to 40 characters after trimming, with runs of whitespace collapsed. There is no Unicode normalization, which would add the server's first `golang.org/x` dependency; the character rules already refuse what matters.
- Letters, marks, digits, spaces, and `. , ' - & ( ) / #`. Control characters, bidi overrides, and emoji are refused.
- No URLs or email addresses: anything with `://`, `www.`, or `@` followed by a dot is refused.
- A short reserved list. "Open Waters", "openwaters", and "aiscast" are refused anywhere in a name. "Official", "admin", "moderator", "USCG", "Coast Guard", and "NOAA" are refused as whole words, so "Badminton Bay" passes. Matching ignores case. The list lives in the server and can change. A station that needs one of these, such as a real Coast Guard receiver, is named by hand in the `stations` table.
- Unique among operator-set names, ignoring case. The first station to take a name keeps it. This stops the cheapest impersonation, copying the top station's name. Vessel names may repeat, because two boats can share a name.
- There is no separate place field. The coverage label gives every station a place, and an operator who wants a particular one can put it in the name.

### Where operators set it

| Option | Covers | Cost | Verdict |
| --- | --- | --- | --- |
| Token page | AIS-catcher over MQTT or HTTP, docker-shipfeeder with a token, bound UDP addresses. Only in the browser that minted the token, because that is where the key is. | A name field beside the generate button, and a signature on the mint request. It lives in the website repo. | **Yes, first.** It is where token holders already are, and it already holds the key. |
| Signal K plugin, using the configured vessel name | Plugin stations with own-ship sharing on, including boats with no MMSI or no transponder | A signed re-mint when the vessel name changes, and a plugin release. No new setting. | **Yes, second.** Boats that send `!AIVDO` already get a name from it, so this fills the gaps and names them before static data arrives. |
| AIS-catcher field (`ID`, which is `stationid` in the HTTP envelope, or the MQTT username) | Only HTTP for `ID`. The preferred MQTT path sends no envelope. | Small | **No.** It is bearer-only and has the hijack hole. The docs tell everyone to use `x` as the username, and `ID` is often a serial number or an aiscatcher.org id. |
| URL parameter (`?name=`) | Whatever URL the operator can edit | Small | **No.** It is bearer-only, and names end up in access logs. |

**The simplest path is the token page.** It is one field in the website repo plus a few lines in `mintPersonal`. It covers every token station whose operator still has the browser that minted the token. An operator who lost that browser's storage mints a new key on the token page and gets a new station id. The page should say so. Losing the old id costs 7 days of hourly history at most, because that is all a station row keeps.

**UDP stations.** Binding and naming in one mint works when the browser shares the receiver's public address, which is the usual home-network case. The name follows the address, not the receiver. A home connection with a changing address loses the name when the address changes. The feeder tier has the same limit today. The remedy for both is to feed with a token over MQTT or HTTP, and the token page should say so beside the bind checkbox. UDP stations behind a shared address (CGNAT, marina wifi) already appear as one station, and naming does not change that.

## Proof of possession

Tokens stay bearer tokens. AIS-catcher's MQTT and HTTP outputs cannot sign anything, so proof has to happen once, when the token is minted. After that, holding the token is enough, as it is today.

**What the gap allows today.** Anyone can mint a token for a station's published key. With it they can:

- publish under that station's id, which puts their data in its counts and on its page,
- earn the feeder tier from the real station's traffic, because `contributed24h` counts by sub,
- take the station's stream slots, because connections are capped per sub, which locks the real plugin out of receiving traffic.

None of this has been seen. It is cheap to do, and names make station ids worth taking.

**The signed mint.** Every mint request may carry `ts` and `sig`. The signature is by the request's public key over the plain lines `aiscast-key`, `pubkey`, `ts`, `bind_ip` (`0` or `1`), `name`, and `vessel_name`. `bind_ip` is signed so that a captured request cannot be replayed from another address to bind it. `ts` must be within 5 minutes of the server clock and later than the last accepted `ts` for that key, so a request cannot be replayed at all. This is the same request as [Naming at mint](#naming-at-mint), with the signature allowed on every mint instead of only named ones. The `/v1/stream` register frame carries the same fields.

**Rollout without breaking installed clients.**

1. **The server accepts signed mints, and a key that has signed once must always sign.** The first signed mint for a key records `signed_at` in its `stations` row. From then on, unsigned mints for that key are refused. Each operator is protected from their first signed mint onward, and clients that do not sign yet keep working.
2. **The token page signs every mint.** It already keeps the private key in `localStorage`, and WebCrypto signs with Ed25519 in the browsers the page already requires. This reaches everyone who visits the page, the day the website deploys.
3. **The Signal K plugin signs every mint, and mints once after the upgrade** so its key is locked even if its name never changes. Node signs with the key in `identity.json`. The server README also names a chart plugin that mints. No caller turned up in the local repos, so it needs checking.
4. **Documentation.** The README, [server/README.md](../server/README.md), and the API reference in the website repo show the signed request for anyone who mints with their own code.
5. **Unsigned mints are refused.** A counter, `aiscast_keys_minted_total{signed}`, shows when unsigned mints have stopped. Signal K users update slowly, so this waits until the counter is near zero rather than a fixed date. A refused mint answers with a message telling the operator to update.

**Tokens minted before the lock stay valid.** They never expire, so a token anyone minted for a key before its first signed mint still works afterward. Refusing every token issued before `signed_at` would also stop the operator's own AIS-catcher, whose token sits in its config. A later option is a signed "revoke older tokens" request that sets a per-key minimum `iat`, which `verify` would check. That option is offered on the token page beside a warning to update the receiver's config. With no sign of abuse, it can wait.

## Email (#51, question 3)

Do not collect email.

- **Abuse and moderation.** Verification does not stop an offensive name, because throwaway addresses are free. The controls that work are the name rules, uniqueness, the reserved list, and `LOCKED_STATIONS` driven by reports to the published contact address. None of them needs email.
- **Reaching operators.** Outages show on the station page (`last_seen`) and in the plugin's status line. The monthly digest is a public post built from `/v1/stations`, which is where named stations get their credit. At about 20 stations, hello@ and GitHub Discussions reach anyone who wants to be reached.
- **Privacy.** The service holds no account data today, and "No account" is on the token page and in the plugin README. Storing email would make contributors identifiable. It would need a lawful basis (consent), a stated purpose and retention in the policy, a deletion path, and care that the address never reaches `/v1/stations`, logs, or backups that are shared. It would also need unsubscribe handling for a digest.
- **Cost.** A mail provider, SPF, DKIM, and DMARC for openwaters.io, a verification flow with expiring links, resend limits so the form cannot be used to spam strangers, and bounce handling. That is several days of work and a new thing to operate, for 19 stations.

Revisit when operators ask for outage alerts, or at about 100 named stations. The shape then is optional email for alerts only, verified, never shown, and removable by the same signed request. A per-station feed or webhook that needs no personal data is worth considering first.

## Phases

### Phase 1: heard-first count

Server only. No new state beyond the 24-hour maps.

- [x] `Own` flag on `Event`, set for `VDO` sentences in `ingestLine`. Each station records the MMSIs it sent as own, and `exclusive` skips them. Phase 2 leaves them out of the coverage point, and phase 3 builds the own-vessel rules on the same record.
- [x] 24-hour retention in `sweep`. `vessels` and `vesselsBySource` count only the last `vesselTTL`.
- [x] `exclusive(now)`, grouped by base station id and cached for 60 seconds. `vessels_24h` and `vessels_exclusive_24h` on rows.
- [x] 24-hour maps saved at shutdown and restored at start (`STATION_VESSELS`, `station-vessels.json`).
- [x] Tests: exclusivity across stations, grouping of `/tag` rows, a duplicate counts as heard, a stale AISHub echo does not remove exclusivity, own vessel excluded, 30-minute `vessels` unchanged.
- [x] Benchmark memory with production-like set sizes. Measured: 9.5 MB for 150,000 entries, and 7 ms for an uncached count.
- [x] [server/openapi.json](../server/openapi.json) and the client `Station` type. The station page gets a "Unique vessels" tile and the stations list can sort by it.

### Phase 2: coverage labels

- [x] Build script that trims GeoNames `cities5000` and adds region and country names, and the embedded file.
- [x] Median point from the positions in the 24-hour vessel map, the 5-vessel minimum, and the place rule: largest within 10 km, nearest within 25 km, region within 100 km. Recomputed every 10 minutes.
- [x] A `stations` table in `aiscast.db`, created with the store schema, holding the last label per station. Without a store (`STORE=off`), labels are computed but not kept.
- [x] `near` on volunteer rows. Tests: median with an outlier, fewer than 5 vessels keeps the old label, each step of the place rule, nothing beyond 100 km, and US and non-US formats.
- [x] Client: "Near Santa Monica, CA" as the title of an unnamed station, with the id suffix where two titles match. The stations list shows the same.
- [x] GeoNames credit in the README and the API reference. The policy sentence above.

### Phase 3: own-vessel names

- [x] Own MMSIs per base station, decided each minute from the stations' `!AIVDO` record with the one-hour rule for two MMSIs. UDP keeps `ownOf`.
- [x] Own MMSIs in the `stations` table.
- [x] Rows gain `name`, `name_from` (`vessel` or `operator`), and `mmsi`, each present only when known.
- [x] Client: the name replaces the coverage label as the title, and an "Own vessel" fact links to the vessel.
- [x] Policy and contributor agreement wording, as above.

### Phase 4: operator names on the token page

- [x] `name`, `vessel_name`, `ts`, and `sig` in `mintPersonal`, with `name_error` in the response, for `/v1/keys` and the register frame: signature, timestamp, rules, uniqueness, limits, bind-address naming, and `LOCKED_STATIONS`.
- [x] Tests: an unsigned mint without a name still works, an unsigned or badly signed mint with a name is refused, a replayed `ts`, a name taken by another station, a locked station, and bound UDP naming.
- [x] Signed mints for every request, the lock once a key has signed (`signed_at`), and the `aiscast_keys_minted_total{signed}` counter.
- [x] Token page (website repo): a name field beside the generate button. Every mint is signed with the saved device key. It says that the name is public and that losing this browser's storage means a new station. The station link already uses `station:` on the website's main branch.
- [x] [README.md](../README.md): "Ask for a named station" becomes "Name your station on the token page".

### Phase 5: Signal K plugin sends its vessel name

- [x] The plugin signs every mint with `identity.json`, and mints once after the upgrade.
- [x] The plugin reads the vessel `name` from Signal K. While "Share my own ship" is on, it sends it as `vessel_name` on a mint signed with `identity.json`. It checks at start, which is when a name or switch change takes effect: Signal K restarts the plugin when its settings change, and a vessel name change needs a server restart. It mints again when the name differs from the one it last sent, and once after the upgrade. Turning the switch off mints with an empty `vessel_name`, which clears it.
- [x] Plugin README: one line under the own-ship setting saying that it also names the station after the boat. CHANGELOG. The release follows CONTRIBUTING.md.

### Phase 6: refuse unsigned mints

- [ ] Once `aiscast_keys_minted_total{signed="false"}` is near zero, `/v1/keys` and the register frame refuse unsigned requests with a message to update.
- [ ] README, [server/README.md](../server/README.md), and the website API reference show only the signed request.

## Found while planning

- **`first_seen` and `duplicates` reset at every restart.** Every row showed `first_seen` 2026-09-30 today. A leaderboard that shows "feeding since" needs `first_seen` saved. The `stations` table is the place for it.
- **A UDP station re-keyed to `mmsi:<n>` earns no feeder tier.** `contributed24h` looks only at `udp:<hash>` for bound addresses, and after re-keying the events land on `mmsi:<n>`.
