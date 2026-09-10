# AIS Privacy Policy

Open Water Software, LLC ("us"/"we") operates the AIS network at ais.openwaters.io. This policy states what personal data the network touches and what we do about it.

## Summary

We publish what vessels broadcast. The track of a small craft can identify its owner, so owners can ask us to suppress their vessel, and we grant those requests. We keep no accounts and collect no contact information. Write to hello@openwaters.io.

## What we publish

AIS is an open radio broadcast. Every equipped vessel transmits its position, course, speed, identity (MMSI), and name, unencrypted, so that other vessels can see and avoid it. We receive these broadcasts through government feeds and volunteer stations. We publish them as a live stream, current positions, and a historical archive, under open licenses.

## When vessel data is personal data

Most AIS traffic is commercial shipping, and that is not personal data. A small craft tied to an identifiable person is different: combined with a vessel registry, its track can show where that person is.

We publish this data on the basis of legitimate interest. The vessel's own equipment broadcasts it publicly, anyone with a receiver can collect it, and it serves navigation, safety, research, and accident investigation. The safeguard that balances this is the opt-out below.

## Vessel opt-out

If your vessel is a small craft tied to you, you can ask us to suppress it. Email hello@openwaters.io with the MMSI and something that shows your connection to the vessel. Any reasonable showing works: a photo, an insurance or mooring document, a club listing. You do not have to be the registered owner. If a company vessel is effectively yours alone, that counts.

Suppression is free. Within a month we remove the vessel from the live feed and from history queries, delete it from the archive we control, and confirm in writing.

The limits, stated plainly: copies of the archive already published under open licenses are beyond recall. Anyone with a receiver can still receive your transponder directly. Other tracking networks are separate, and you must contact them separately.

Commercial traffic whose AIS carriage is mandated is not covered by the opt-out.

## Station operators

We identify a station by its public key, or by a keyed hash of its network address for UDP senders. We do not collect names, email addresses, or accounts. We never ask where your station is, and we publish no location for it. The data you send can reveal your position itself; the [contributor agreement](contributor-agreement.md) says how.

## Visitors and API users

The map and the API need no account. A token is a key your browser generates and keeps. We use network addresses to enforce rate limits and to respond to abuse, and for nothing else.

## Retention

Receptions are the historical record and are kept indefinitely. Suppressed vessels are deleted from history and from the archive we control, per the opt-out above.

## Your rights

If the GDPR applies to you, the opt-out above is your right to object, and the deletion that follows is your right to erasure. You can also complain to your data protection authority.

## Changes

This policy lives in the [aiscast](https://github.com/openwatersio/aiscast) public repository. We revise it only by pull request there.
