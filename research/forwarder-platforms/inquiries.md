# Vendor inquiry drafts

Drafts only — nothing here gets sent without explicit go-ahead. Addresses and context per target are in the beat files in this directory. Adjust sender details before sending; these are written to come from Brandon at Open Waters (ais.openwaters.io).

## Wave 1: the pipe already exists

### TimeZero / Nobeltec (mytimezero.com/contact)

Subject: Adding an open AIS network as a TZ Online AIS destination

Hi — I run aiscast (ais.openwaters.io), an open AIS aggregation service: volunteer receivers feed it, and the aggregate goes back out free under an open license, unlike the commercial trackers.

TimeZero already has exactly the plumbing I'm writing about: the TZ Online AIS option that shares a user's locally-received AIS with the community layer. Would you consider letting users also (or alternatively) point that uplink at an open network? On our side it's a UDP or HTTPS endpoint that accepts plain NMEA, and TZ users who opt in would get an open feed and a public coverage map back rather than a one-way donation.

Happy to share technical details or usage data. Who's the right person to talk to about this?

### Weather4D (weather4d.com/contact)

Subject: Weather4D's AIS sharing — adding an open network alongside AISHub

Bonjour Francis — I run aiscast (ais.openwaters.io), an open AIS aggregation service with an openly-licensed aggregate feed. Weather4D's AIS sharing via your server into AISHub is, as far as I can tell, the best example anywhere of a nav app uplinking users' received AIS to a community network.

Would you be open to adding aiscast as an additional destination from your server? We accept plain NMEA over UDP (ais.openwaters.io:10110) or gzip'd HTTPS POST, and we publish station stats and a coverage map your users could see. Glad to do the integration work on our side or provide whatever endpoint shape is easiest for you.

### Pocket Mariner (coverage@pocketmariner.com)

Subject: Adding aiscast to your downstream feeds

Hi — I run aiscast (ais.openwaters.io), an open AIS aggregation service. I understand Boat Beacon's network already forwards to AISHub, MarineTraffic, and ShipFinder. Would you add aiscast as another downstream? We take plain NMEA over UDP or HTTPS, we're happy to attribute Pocket Mariner as a source, and our aggregate is redistributed under an open license, so your data would be reaching the open-data side of the ecosystem too. Can set up a dedicated ingest port/token for you.

### Yacht Devices (agorlach@yachtdevices.com)

Subject: Outgoing connection to a DNS hostname on YDWG/YDEN/YDNR

Hi Aleksandr — I run aiscast (ais.openwaters.io), an open AIS aggregation network fed by volunteer receivers. Your gateways' outgoing-connection feature looks like the only one in the marine gateway market that can push NMEA to an internet host with no onboard computer, and I'd like to document a "feed aiscast straight from a Yacht Devices gateway" recipe for boaters and marinas.

Two questions: does the outgoing connection's address field accept a DNS hostname (e.g. ais.openwaters.io), or numeric IPs only? And if numeric-only, is hostname support feasible in a firmware update? Happy to test firmware builds or feature your gateways in our contributor guide.

### Airframes (code@airframes.io, or Discord)

Subject: aiscast × Airframes — open AIS data exchange

Hi — I run aiscast (ais.openwaters.io), an open AIS aggregation service (think open-data replacement for aisstream.io: volunteer feeders in, openly-licensed aggregate out, full history kept). I saw your AIS-catcher HTTP ingest is live and that marine output endpoints are on the way.

Since we're both building on the same feeder ecosystem with an open posture, is there appetite for a data exchange or cross-listing — e.g. we exchange aggregates or point feeders at both networks in our respective docs? No strings; mostly want to avoid the open-AIS community fragmenting into silos the way the commercial side did.

## Wave 2 template (savvy navvy, Aqua Map/GEC, SEAiq, Rose Point, Actisense)

Subject: Feature request: forward received AIS to an open network

Hi — I run aiscast (ais.openwaters.io), an open AIS aggregation service. [App] already receives AIS from users' own receivers over [NMEA Connect / WiFi connections / the NMEA port]; several vendors (TimeZero, Weather4D, Pocket Mariner) already let that data feed community AIS networks. Would you consider an opt-in "share AIS" setting pointing at aiscast? It's one UDP or HTTPS destination on your side; users who opt in get an open aggregate feed and a public station/coverage page back. Happy to provide the endpoint spec, test tokens, and engineering help.

Per-target notes:

- **savvy navvy** (help.savvy-navvy.com ticket): lead with the Actisense NMEA Connect partnership as evidence they build integrations; largest reachable user base of the wave.
- **Aqua Map / GEC** (support@aquamap.app): small responsive shop; ask specifically whether the NMEA layer could add a UDP forward-out target.
- **SEAiq** (info@seaiq.com): different ask — they already ingest base-station feeds on request; offer aiscast as an inbound network feed for their users, and separately suggest extending their NMEA server (currently GNSS-only) to include AIS.
- **Rose Point** (community.rosepoint.com / verify joe@rosepoint.com): ask for remote-unicast on Coastal Explorer's UDP output or a Nemo Gateway cloud destination; their UDP output already passes AIVDM, it's only broadcast-scoped.
- **Actisense** (sales@actisense.com): ask whether W2K-2 has or could add an outbound TCP/UDP client mode like Yacht Devices' outgoing connection.
