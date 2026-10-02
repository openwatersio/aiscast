# Existing docs and guides where aiscast can get listed

Researched 2026-08-24. Strategy: before writing our own setup guides, get aiscast linked from the documentation people already find — platform docs first, then third-party guides. Split by edit mechanism: direct PR, open wiki (access uncertain), or author/vendor outreach.

## Master list

| Doc / guide | Platform | Mechanism | Priority | Where aiscast fits |
|---|---|---|---|---|
| Signal K "Roaming AIS Station" blog post | Signal K | **we own it** | high | update the post directly |
| AIS-catcher docs (`jvde-github/AIS-catcher-docs`) | AIS-catcher | PR | high | no provider list exists — edit the aggregator name-drops in `managed/output.md` + `usage/cli.md`, worked `-H` example in `HTTP.md` |
| docker-shipfeeder README (`sdr-enthusiasts/docker-shipfeeder`) | AIS-catcher/Docker | PR | high | aggregator table row (+ optionally env var in s6 script, like the SDRMap PR) |
| SARCNET AIS receiver page | station guide | outreach | high | its aggregator connection-string list (8 entries today; updated 2025-12-01) |
| OpenCPN dokuwiki Connections + ais-software pages | OpenCPN | wiki (registration unverified) | high | next to the existing AIShub forwarding note |
| OpenPlotter docs (`openplotter/docs`) | OpenPlotter | PR | medium | "forwarding to aggregators" note in `docs/sdr-vhf/sdr-vhf_app.md` (currently a stub awaiting the 4.x rewrite — good timing) |
| AIS-catcher for Android README | AIS-catcher Android | PR | medium | UDP-output section (no preset list exists in the repo) |
| SDRangel AIS Demod readme (`f4exb/sdrangel`) | SDRangel | PR | medium | prose near the UDP output section (aggregator mention may read promotional — keep it neutral) |
| Pi Stack self-hosted AIS guide (2026-06-06) | station guide | outreach | medium | its AIS-catcher forwarding-targets list |
| TheFloatingLab AISdispatcher.pl (`TheFloatingLab/AISdispatcher.pl`) | dispatcher tool | PR | medium | built-in output target alongside AISHub/MarineTraffic/etc. |
| rtl-sdr.com AIS tutorial | station guide | outreach | medium | alternative to its MarineTraffic UDP-sharing mention (top search hit, but stale content) |
| qtVlm documentation PDF / forum | qtVlm | outreach | medium | document aiscast as an NMEA-proxy UDP destination |
| Victron Venus OS Large wiki + community forum | Venus OS | wiki (access unverified) / forum | medium | Signal K section of the Large page; or reply in the existing "AIS to aishub via SignalK" thread |
| Panbo (DataHub AIS-sharing post, forum) | boater audience | outreach | medium | follow-up/forum post noting aiscast beside the six services DataHub feeds |
| David Burch "AIS in qtVlm" blog post | qtVlm | outreach | low | aggregator examples paragraph |
| Expedition manual PDF / forum | Expedition | outreach | low | document the "UDP to IP address" output with aiscast as an example |
| ShipPlotter FAQ (coaa.co.uk) | ShipPlotter | outreach | low | data-sharing/output section |
| jeremyclark.ca SDRangel+OpenCPN guide | SDRangel | outreach | low | an "also feed an aggregator" addendum |
| BBN docs (`bareboat-necessities/my-bareboat`) | BBN | PR (single-maintainer; outreach in practice) | low | forwarding subsection near the AIS/Signal K material |
| wiki.luntti.net RTL-SDR AIS page | station guide | wiki (registration unverified) | low | new "feed aggregators" subsection |
| AIS Dispatcher page (aishub.net) | AIS Dispatcher | outreach | low | listed destination beside AISHub/VesselFinder (competitor-controlled; unlikely) |
| awesome-mda, awesome-SDR | GitHub lists | PR | low | data-sources/AIS section if one exists (unverified) |
| RadioReference wiki AIS page | radio hobbyists | wiki (page protected) | low | AISHub mention area; needs vetted account |
| Wikipedia AIS article | general | **skip** | — | names no aggregators at all; adding one reads promotional and will be reverted |

## We can edit directly

### Signal K blog — ours
`SignalK/signalk.github.io`, post source `_posts/2025/roaming-ais-station.mdx` ("Running a Roaming AIS Station", authored by Brandon). Maintainer access already in hand — just edit.

### AIS-catcher docs — PR
Repo `jvde-github/AIS-catcher-docs` (MkDocs behind jvde-github.github.io/AIS-catcher-docs). **No provider/aggregator directory exists anywhere in the docs** (verified by grep of the full tree 2026-08-24; the "Aggregator" profile in `configuration/overview.md` is about AIS-catcher routing streams, not services). Aggregator services appear in exactly three passing spots, which are the edit targets:

- `docs/managed/output.md` — "**UDP** — the workhorse for feeding services like MarineTraffic and VesselFinder" (the setup-wizard page new users follow; a three-word edit adds aiscast)
- `docs/usage/cli.md` line ~85 — "AIS aggregator websites such as MarineTraffic or VesselFinder"
- `docs/configuration/output/HTTP.md` — worked `-H` examples for APRS.fi and Airframes.io; aiscast fits as another worked example since it accepts the default `AISCATCHER` protocol (`-H https://ais.openwaters.io/v1/receive USERPWD x:<token> GZIP on INTERVAL 15`)

A dedicated "Feeding aggregators" page is a genuine gap in their docs — worth proposing via issue/discussion before writing it. External doc PRs merge (PR #1, typo fix by shawaj); the main AIS-catcher repo merges outside PRs routinely (#619, #595, #543, #524, #510, #493, #405).

### docker-shipfeeder — PR
`sdr-enthusiasts/docker-shipfeeder`, README.md aggregator table at lines 241–259 (Name / Parameter / Default IP:port / Protocol / How to register). Precedent: merged PR #83 "Add SDRMap" added a table row plus an env var in the s6-overlay aiscatcher script — the model to follow for a first-class `AISCAST_*` variable (though `UDP_FEEDS=ais.openwaters.io:10110` already works without any change). Maintainers kx1t and fredclausen; also a companion Google Sheet of aggregators linked from the table intro (edit access unverified).

### AIS-catcher for Android — PR
`jvde-github/AIS-catcher-for-Android` README UDP-output section (currently documents only local plotting apps; no preset list exists in the code — the "presets" seen in setup guides are just documented port conventions). External PRs merge (#24, #23, #17, #14).

### SDRangel — PR
`f4exb/sdrangel`, `plugins/channelrx/demodais/readme.md`. UDP/NMEA output documented but no aggregator named; small external doc PRs merge (Daniele Forsi's typo fixes). Keep the addition neutral ("the NMEA output can feed network aggregators such as …") to avoid reading as promotion. (acceptance unverified)

### OpenPlotter docs — PR
`openplotter/docs` (MkDocs behind openplotter.readthedocs.io). Target `docs/sdr-vhf/sdr-vhf_app.md` — currently a "Coming soon" stub while the 4.x rewrite is pending, so a PR adding a forwarding section lands at the right moment. External PRs merge (#18, #17, #12, #9, #6, #5, #4, #3); the docs' "How to collaborate" page documents fork-and-PR, with the OpenMarine forum as fallback.

### TheFloatingLab AISdispatcher.pl — PR
`TheFloatingLab/AISdispatcher.pl` (Frans Veldman): Perl dispatcher with built-in outputs for MarineTraffic, MyShipTracking, FleetMon, VesselFinder, AISHub, ShipFinder. A PR adding aiscast as a target mirrors docker-shipfeeder. (maintenance activity unverified)

### awesome-mda / awesome-SDR — PR, low value
[awesome-mda](https://github.com/mnitin73/awesome-mda), [awesome-SDR](https://github.com/CanYoleri/awesome-SDR). No dedicated awesome-ais list exists. Check whether either has a section aiscast fits before bothering. (unverified)

## Open wikis, access uncertain

- **OpenCPN dokuwiki** — the canonical user manual still lives on the dokuwiki (the GitHub-hosted opencpn-manuals org covers developer + plugin manuals only and links out to the dokuwiki for the Basic Manual; no AIS-forwarding content exists in any `OpenCPN/*` or `opencpn-manuals/*` repo). Login page shows no self-registration link (unverified) — fallback is asking on the cruisersforum.com OpenCPN section for wiki access. Targets: the [Connections page](https://opencpn.org/wiki/dokuwiki/doku.php?id=opencpn:manual_basic:set_options:connections) (which already says "forwarding AIS messages to … consumers like AIShub") and [ais-software](https://opencpn.org/wiki/dokuwiki/doku.php?id=opencpn:supplementary_software:ais-software).
- **Victron Venus OS Large page** — Victron-run dokuwiki (`venus-os/large.txt`), registration policy unverified; the practical channel is community.victronenergy.com, which has an on-topic thread: ["Can't get AIS data to aishub.net through SignalK"](https://community.victronenergy.com/t/cant-get-ais-data-to-aishub-net-through-signalk/56183).
- **wiki.luntti.net** — MediaWiki, last edited 2026-05-08, login link present, registration policy unverified.
- **RadioReference wiki AIS page** — protected ("view source only"); needs their account/edit-request process. Low priority.

## Outreach to author or vendor

- **SARCNET** (sarcnet.org/ais-receiver.html) — Julie VK3FOWL and Joe VK3YSP; page updated 2025-12-01 and already lists connection strings for AISHub, VesselFinder, Pocket Mariner, ShipFinder, MarineTraffic, Shipping Explorer, AIS Friends, MyShipTracking. The single best-fit outreach: one more row in an existing list. No direct email on the page — use sarcnet.org's main contact. (contact unverified)
- **Pi Stack** (pistack.xyz, 2026-06-06 guide) — names AISHub/MarineTraffic/VesselFinder as AIS-catcher targets; fresh and relevant. Contact via footer GitHub/social links. (unverified)
- **rtl-sdr.com** — top search hit for AIS tutorials (stale content, but the traffic is real). Contact via [Submit-a-Story/Contact](https://www.rtl-sdr.com/submit-a-storycontact/). Angle: they cover new open-data SDR projects as news posts too — a "new open AIS network" story may do more than an edit to the old tutorial.
- **qtVlm / Meltemus** — contact@meltemus.com, or the [NMEA proxy forum thread](https://www.meltemus.com/index.php/en/forum/qtvlm-application/208-configure-qtvlm-as-nmea-proxy) where admin "maitai" is responsive. Ask: document aiscast as a proxy/UDP destination in the PDF manual.
- **Panbo** — Ben Ellison/Ben Stein; guest entries accepted (no paid links); the [DataHub AIS-sharing post](https://panbo.com/share-your-boats-ais-info-easy-with-datahub-by-predictwind/) names six aggregator destinations; their marine electronics forum is the low-friction channel.
- **David Burch / Starpath** ([AIS in qtVlm post](http://davidburchnavigation.blogspot.com/2022/05/qtVlm-AIS.html)) — runs his own Seattle shore AIS antenna, so he's also a prospective feeder; blog comments or starpath.com contact.
- **Expedition** — post on expedition.boardhost.com where Nick White answers directly (the ["Copy on UDP port" thread](https://expedition.boardhost.com/viewtopic.php?pid=6180) is the relevant one); doubles as the channel for verifying WAN behavior of "UDP to IP address".
- **ShipPlotter / COAA** — support@shipplotter.com; ask for an aiscast note in the FAQ's output/sharing section.
- **jeremyclark.ca** — Jeremy Clark VE3PKC, SDRangel+OpenCPN guide (2022); contact link on site. (unverified)
- **AISHub's AIS Dispatcher page** — vendor-controlled by the AISHub/VesselFinder family; listing a competitor is unlikely. Only worth raising inside the existing member relationship, if at all.
- **worldwideais.org** — flagged discrepancy: their public site now reads as a token-incentive network pitch (Worldwide AIS Network ApS, Copenhagen; WAKE token rewards), and the setup guides surveyed 2026-08-22 weren't findable from the homepage this pass. Re-check before engaging. contact@worldwideais.org.
- **Pocket Mariner blog** — old SARCNET post; already covered by the Wave-1 partnership inquiry, no separate docs outreach needed.

## Skip

- **Wikipedia AIS article** — currently names no individual aggregator; WP:ELNO/WP:NOTLINK make an aiscast addition read as promotion with high revert risk. Not a channel.
- **MarineTraffic's ShipPlotter/NMEA guides** — competitor-controlled support articles; no.
