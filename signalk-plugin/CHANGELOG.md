# Changelog

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
