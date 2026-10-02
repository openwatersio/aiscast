# Open Waters AIS privacy policy

Effective [LAUNCH DATE].

Open Water Software, LLC ("we") runs Open Waters AIS: the map at openwaters.io/ais/vessels, the API at ais.openwaters.io, and the network of stations that feed it. This policy says what personal data the service handles, why, how long we keep it, and what you can ask us to do. Write to hello@openwaters.io with any question or request. A person reads it.

## Summary

We publish what vessels broadcast over AIS. The track of a small boat can point to its owner, so an owner can ask us to stop publishing their vessel, and we will. We have no accounts and ask for no email address or payment details. We log API requests to keep the service running and to stop abuse. We do not sell personal data, and the map runs no analytics, advertising, or tracking scripts.

## Vessel data

AIS is an open radio broadcast. Vessels send their identity (MMSI), name, call sign, position, course, speed, destination, and dimensions, unencrypted, so that other vessels can see them. We receive these broadcasts from government open-data feeds, other AIS exchanges, and volunteer stations. We add public registry details, such as build year, tonnage, owner, operator, and home port, from Wikidata and the US Coast Guard.

We publish this data as a live stream, current positions, vessel tracks, and an archive, under open licenses. We keep each vessel's last known position and details, and every reception in our archive, indefinitely. Our server also keeps every position from the past two to three days, to answer track queries quickly.

Most AIS traffic is commercial shipping, and that is not personal data. A small craft linked to a person is different. A vessel registry can connect the MMSI to an owner, and the track can then show where that person is or lives.

We publish this data on the basis of legitimate interest. The vessel's own equipment broadcasts it to anyone in range. Publishing it supports safety, navigation, search and rescue, research, and accident investigation. The opt-out below is the safeguard that balances this for small craft.

## Vessel opt-out

If your vessel is a small craft linked to you, you can ask us to stop publishing it. Email hello@openwaters.io with the MMSI and anything that shows your connection to the vessel. A photo, an insurance or mooring document, or a club listing is enough. You do not have to be the registered owner. A vessel registered to a company counts if you are its only user.

The opt-out is free, and a person handles each request. Within 30 days we stop publishing the vessel in the live stream, on the map, and in track and history queries. We delete it from the archive we control and confirm this to you in writing. We keep the MMSI on a list so that we do not publish the vessel again. We delete the proof you sent once we have handled the request.

There are limits. Copies of the archive that others took under an open license are beyond our control. Anyone with a receiver can still hear your transponder. Other tracking sites are separate, and you must ask each of them.

The opt-out does not cover vessels that must carry AIS by law, such as commercial ships.

## Station operators

A volunteer station is identified by a public key that its software generates, or, for stations that send over UDP, by a keyed hash of the sender's address. We never ask for a station's location. We label each station with the town of 5,000 or more people, or the region, nearest to the traffic it hears. The stations list also shows the area covered by all the positions a station has heard, which can include its own vessel's positions.

A station can have a public name. It comes from the name the operator types on the token page, or else the vessel name the Signal K plugin sends, or else the name of the station's own vessel. We store these names and show them on the stations list, with the station's own vessel if it has one. To change or clear a name, use the token page again or write to us.

If your station sends your own vessel's position (`!AIVDO`), or the Signal K plugin builds one for you, we publish it like any other vessel, and we identify your station by that vessel. The Signal K plugin is off until you turn it on. Once it is on, sharing your own position is on by default, and you can turn it off. The data you send can reveal where your station is. The [contributor agreement](https://github.com/openwatersio/aiscast/blob/main/docs/contributor-agreement.md) explains this.

When a station sends data to our API, our request logs link its key to its network address, as described below.

We forward receptions from volunteer stations to AISHub, an AIS exchange, as part of an exchange agreement.

## Tokens

You do not need a token. A free token, from the [token page](https://openwaters.io/ais/token) or the Signal K plugin, raises your limits. Your browser or plugin generates a key pair, and the private key stays on your device. We sign a token for the public key and send it back. We do not keep a copy of the token. For each station that signs its requests, we store its names, its own vessel's MMSI, and the times of its first and last signed requests.

If you choose to bind a token to your address, that address is written into the token. The token is then valid only from that address, and your UDP station is linked to your token.

## The map

The map at openwaters.io/ais/vessels, with its vessel, station, and network pages, needs no account. It does not use analytics.

- **Cookies and storage.** If you pick a theme, the map sets a cookie, `aiscast-theme`, that remembers your choice for a year. The map keeps your last search in your browser until you close the tab. The token page keeps your key pair, token, and station name in your browser's local storage. Nothing else is stored, and you can delete all of it by clearing site data.
- **Starting location.** Cloudflare estimates your approximate location from your IP address. The map uses that estimate to open near you. We do not store it.
- **Your location.** If you choose "Near me" or the locate button, your browser asks for your permission and then shares your location with the map. The locate button keeps it in your browser. A search sorted by distance sends your location, or the center of the map, to our API. The map also sends the area on your screen. Our request logs keep these only to about 10 km.
- **Other services.** The map loads its base map from [OpenFreeMap](https://openfreemap.org/), and vessel photos straight from [Wikimedia Commons](https://commons.wikimedia.org/). Those services receive your IP address and the site you came from. Their own privacy policies apply. The map's server looks up which photos exist, so Wikimedia does not see those lookups come from you.

The other pages on openwaters.io, including the Open Waters AIS home page, are covered by the [Open Waters privacy page](https://openwaters.io/privacy/).

## Request logs

Every request to the API at ais.openwaters.io is logged twice. A live stream or MQTT connection is logged once, when it closes. Data that stations send over UDP is not logged this way. Neither log records the body of a request, such as what an AI assistant asks our MCP server to look up.

- **The web server log** holds your full IP address, the request, and your browser's request headers. It leaves out tokens, the `Cookie` and `Referer` headers, your search position, and the map area you asked for. It keeps the exact map tiles you load, which show the area you are looking at. It stays on our server and is deleted within two weeks.
- **The API log** holds the request, including any search text, vessel, or station it names, the time, the result, your network (the first three parts of an IPv4 address, or the first 48 bits of an IPv6 address), a keyed hash of your full address, your browser's user agent, the site that sent you without its query, the `Origin` header, and your token's public identifier. It leaves out tokens. It keeps locations in the request, such as a search position, the map area, or a map tile, only to about 10 km. The hash links your requests over time, so we can follow abuse across days and weeks. Because we hold the key, we can recover your address from this log. We delete it after 90 days.

We use these logs to find what is causing load, to detect and stop abuse, to learn how the API is used, and to plan new features. We also count requests per address to enforce rate limits. Error messages in the server's system log can include an address. That log has a fixed size, and new entries overwrite old ones.

## Email

If you write to us, we keep the correspondence. We use it only to answer you and to carry out your request.

## Monitoring

We send counts and timings to Grafana Cloud to watch the service's health. They hold no addresses, tokens, or vessel positions. Event counts per source are labeled with each station's public identifier.

## Who else handles data

- **Cloudflare** serves the map and openwaters.io, so it sees those requests. Cloudflare Workers Logs keep a record of each request to the map, with your address, the page, and your browser's headers, for up to seven days. They can include Cloudflare's estimate of your location. We use them to find and fix errors. Our archives and the API log are stored in Cloudflare R2. The API log is in its own private bucket.
- **Hetzner** hosts the API server in Helsinki, Finland. The API does not pass through Cloudflare.
- **Grafana Cloud** stores our monitoring data.

These providers process data for us under their own terms. We do not sell personal data or share it for advertising.

## Your rights

You can ask us what we hold about you, and ask us to correct it or delete it. We have no accounts, so we may ask you for something that lets us find your data, such as your token, your MMSI, or your IP address and the times you used the service.

If the GDPR applies to you, the vessel opt-out is your right to object, and the deletion that follows is your right to erasure. You can complain to your data protection authority.

## Changes

This policy lives in the public [aiscast repository](https://github.com/openwatersio/aiscast/blob/main/docs/privacy-policy.md). We change it only by pull request there, so every change is public. A material change takes effect 30 days after its pull request merges. Watch the repository to hear about changes.
