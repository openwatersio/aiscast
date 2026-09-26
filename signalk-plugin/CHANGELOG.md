# Changelog

## 0.5.2

- Works with aiscast's new names for contributing stations. aiscast now files every contribution as `station:<key>`, which 0.5.1 does not recognize, so it took its own reports coming back from the server for traffic from another station. The loop guard then held back the boat's receiver when it heard an identical report again, such as a nearby vessel's unchanged static data, and that report never reached aiscast. The plugin recognizes its own reports under the old and new names, and with an operator-issued token as well as one it minted itself.

## 0.5.1

- Chartplotters now show names, call signs, and dimensions for aiscast targets. aiscast sends a vessel's static data only when it changes, so for most aggregated targets the only copy arrives in the snapshot on connect, which the NMEA 0183 relay held back. The plugin now keeps the latest static data for each target, sends it right after the target's first live position, and repeats it with a position every 6 minutes while the target stays live.

## 0.5.0

- New Receive setting, on by default: *Send aiscast traffic to NMEA 0183 output*. Targets injected from aiscast are re-emitted as `!AIVDM` on the `nmea0183out` event, so chartplotters and tablet apps reading the server's NMEA 0183 connections see over-the-horizon traffic, not only the Signal K apps. Only aiscast-sourced targets are relayed, and in `Always` mode a target the local receiver already covers is left out, so nothing is duplicated. Turn it off if `signalk-vessels-to-ais` is doing the same conversion.
- Position events older than 2 minutes are discarded before they reach Signal K or NMEA 0183. Static data is always injected into Signal K, but reaches NMEA 0183 only after that vessel has sent a live position, keeping the snapshot replay burst off slow serial connections. Remote `!AIVDO` is converted to `!AIVDM` before relay so it cannot replace the chartplotter's own-ship state.

## 0.4.0

- The offline queue writes far less to the SD card. Each queue file used to be rewritten from scratch every fifteen seconds as sentences were added to it, so a boat at anchor hearing a slow trickle of traffic wrote the same data to the card a dozen times over. Sentences are now appended to the end of a file and nothing is rewritten, which on a quiet anchorage cuts the writing to a fraction of what it was.
- A replay the server only partly accepts no longer rewrites anything on disk, and a queue file damaged by a power cut costs one sentence instead of the five hundred it was sharing a file with.
- The queue keeps working when the plugin cannot delete a file, as happens when an SD card turns read-only. A queue file that could neither be read nor removed used to stop the replay where it stood until the next reconnect, and files the server had already taken could be left behind in a way that made the 100 MB cap discard data that had not been sent yet. Files that will not delete are now set aside, retried in the background, and never replayed.

## 0.3.1

- Fixes a queue that never emptied. While the plugin was replaying a backlog to aiscast it wrote every sentence its receiver heard to a new queue file, so it traded one file for another and the replay never reached the end. On a boat that had been offline a while, the queue directory stayed at tens of thousands of files no matter how long the connection was up. A replay now carries up to a thousand sentences from as many files in one frame, and sentences heard mid-replay go out from memory.
- The queue keeps far fewer files. Time offline used to leave one behind every fifteen seconds; the plugin now fills each file to 500 sentences before starting another, so the count follows how much is owed rather than how long the boat was out of range.
- Sentences past the server's publish limit are no longer lost. aiscast accepts 6000 sentences a minute and quietly drops the rest, so a large replay used to arrive mostly empty while the plugin counted it as delivered. The plugin now paces a replay under the limit and keeps anything the server did not take.

## 0.3.0

- Buddy boats: vessels on [signalk-buddylist-plugin](https://github.com/sbender9/signalk-buddylist-plugin)'s list are followed on aiscast wherever they are, far beyond VHF range, in every receive mode. The buddylist plugin keeps raising its buddy flag and proximity alerts, so Freeboard-SK's buddy icon and phone notifications work at any distance. The status line shows how many buddies have been heard from.
- Own-ship reports from NMEA 2000 are recognized by the boat's MMSI, not only by the transceiver-information field. A transmitting transponder (like the B&G V60-B) stamps its own position "Channel A/B VDL transmission", which previously published as `!AIVDM` and left the plugin unaware a transponder was present. Those reports now publish as `!AIVDO`, pause self-reported position synthesis while transponder data flows, and follow the *Share my own ship's AIS transponder data* setting.

## 0.2.0

- New Share setting: *Fallback to self-reported AIS position*. When an AIS transponder is not available, the plugin builds class B position and static reports from Signal K (like `@signalk/aisreporter`). It publishes them as `!AIVDO` tagged `s:self`, so a boat with only a GPS appears on aiscast as self-reported. Synthesis pauses while the plugin hears a real `!AIVDO`. The setting is checked by default when you save the config form, and configs saved before this setting existed stay off until you save them again. The checkbox is disabled until an MMSI is set in Vessel settings.
- Each Share setting explains itself below its label, and Receive mode renders as radio buttons instead of a dropdown.

## 0.1.4

- Personal tokens minted by aiscast no longer expire. The plugin keeps a cached token with no expiry instead of minting a new one every start.
- The npm package includes this changelog.

## 0.1.3

- Fixes the plugin never connecting on high-latency links (Starlink in remote regions, satellite, some cellular). Node abandoned the IPv4 connection after 250 ms and then used an unroutable IPv6 address. Each attempt now gets 3 s.
- The debug log shows the real reason a socket failed (for example `socket error: ETIMEDOUT (connect ETIMEDOUT 2.29.0.215:443; …)`) instead of an empty `socket error:` line.

## 0.1.2

- Clears the red "No token" line on the Dashboard once the token is obtained.

## 0.1.1

- Retries token minting with backoff (1 to 30 min) instead of waiting six hours after a failed start. A Pi that boots before its network is ready now starts sharing as soon as it can.
- Token errors include the underlying cause (DNS, TLS, connection refused).

## 0.1.0

- First release: shares received AIS (NMEA 0183 and NMEA 2000) with aiscast and injects nearby traffic from aiscast when the boat hears none of its own.
