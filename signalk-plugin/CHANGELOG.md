# Changelog

## 0.1.4

- Personal tokens minted by aiscast no longer expire; the plugin keeps a cached token with no expiry instead of minting a new one every start.
- The npm package includes this changelog.

## 0.1.3

- Fixes the plugin never connecting on high-latency links (Starlink in remote regions, satellite, some cellular): Node gave up on the IPv4 connection after 250 ms and fell back to an unroutable IPv6 address; each attempt now gets 3 s.
- The debug log shows the real reason a socket failed (for example `socket error: ETIMEDOUT (connect ETIMEDOUT 2.29.0.215:443; …)`) instead of an empty `socket error:` line.

## 0.1.2

- Clears the red "No token" line on the Dashboard once the token is obtained.

## 0.1.1

- Retries token minting with backoff (1 to 30 min) instead of waiting six hours after a failed start, so a Pi that boots before its network is up starts sharing as soon as it can.
- Token errors include the underlying cause (DNS, TLS, connection refused).

## 0.1.0

- First release: shares received AIS (NMEA 0183 and NMEA 2000) with aiscast and injects nearby traffic from aiscast when the boat hears none of its own.
