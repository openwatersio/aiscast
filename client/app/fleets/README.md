# Fleets

Each `.yaml` file here is a fleet, at its path under `/ais/fleets/`: `cruise-ships/royal-caribbean.yaml` is `/ais/fleets/cruise-ships/royal-caribbean`. Each folder is a group of fleets, and its `index.yaml` gives the group's `title`, `summary` and optional `description`, and nothing else. Folders nest. The Fleets page is the top group: it shows the top-level folders and files, and each group shows what is in its folder, as cards with the most vessels first, counting every vessel in a group's fleets, and the first shown largest, or the first two when there are only two. A group's cover is the first photo its fleets name.

```yaml
# cruise-ships/index.yaml
title: Cruise ships
summary: The cruise lines and their fleets.
```

A fleet:

```yaml
title: Tall ships # required
summary: Square riggers and schooners still under sail. # required, one line, shown on the card
description: Longer text shown under the title on the fleet's page.
source: # where the members come from, shown as "Source: Wikipedia", linked
  Wikipedia: https://en.wikipedia.org/wiki/Statsraad_Lehmkuhl
cover: Statsraad Lehmkuhl # a vessel with an MMSI whose photo covers the fleet; the first with an MMSI otherwise
photos: # Wikimedia Commons file names that head the fleet's page and cover its card, ahead of `cover`
  - Statsraad Lehmkuhl in Bergen.jpg
vessels:
  - name: Statsraad Lehmkuhl # required
    mmsi: 258113000 # how the map finds the vessel; leave out when unconfirmed
    imo: 5339248 # finds its photos on Wikimedia Commons
    subtitle: Norway · three-masted barque, 1914
    note: A sentence or two about the vessel.
    photos: # Commons files shown with the vessel, such as its interiors
      - Statsraad Lehmkuhl deck.jpg
    links:
      Ship details: https://example.org/ship
      AIS identity: https://example.org/where-the-mmsi-was-checked
```

A fleet whose vessels fall into groups uses `sections` in place of `vessels`. Each section takes a `title`, an optional `subtitle`, `avatar` and `links`, and its own `vessels`. A section with an avatar or links, such as a YouTube channel, is headed by its name, subtitle and avatar, with its links as buttons:

```yaml
sections:
  - title: Gone With The Wynns
    subtitle: Jason & Nikki Wynn
    links:
      YouTube: https://www.youtube.com/@gonewiththewynns
    vessels:
      - name: Undra
        mmsi: 368478440
```

`npm test -w client` checks every file: that it parses, has a title and summary, that every folder has an `index.yaml`, that it uses only the keys above, that each MMSI is a number of nine digits starting 2 to 7 and each IMO a number with a valid check digit, that no MMSI is listed twice in a fleet, and that every link is https.

Photos must be on Wikimedia Commons under a free licence, which every Commons file is; each is shown with its author and licence from Commons. A group's card shows the first photo its first fleet lists, and a fleet's card its own first photo. A photo that cannot be found falls back to the cover vessel's. Commons answers are kept at the edge for a week, so a new photo shows within that time of a deploy.

On a fleet's page, readers can sort its vessels as listed, by when they were last heard, or by name, and show only those on the map or heard in the last day.

An MMSI follows the radio, not the hull, and changes when a vessel changes flag. Link where you confirmed it under `AIS identity`.

## Drafting a fleet from Wikidata

[scripts/fleet-from-wikidata.mjs](../../scripts/fleet-from-wikidata.mjs) drafts a fleet's file from Wikidata: every current ship of an operator, owner, builder or type, grouped by class and checked against the network.

```bash
node client/scripts/fleet-from-wikidata.mjs --operator Q929872 --title "Royal Caribbean" --out client/app/fleets/cruise-ships/royal-caribbean.yaml
```

It keeps every ship, heard or not. Each MMSI is checked against the network: one that reports another ship's IMO, or another name with no IMO to confirm it, is left out, and a ship without an MMSI gets one when the network hears a vessel with its IMO under its name. What needs a person is marked with a `# check:` comment above the ship, such as a ship heard under another name, which is most often a ship sold on that Wikidata still lists. Read the draft, settle each check, delete the comments, and add photos. The summary is written from Wikidata's facts about the operator, owner or builder, such as when it was founded, where it is based and what it is part of, and from the fleet's own ships: how many, and the oldest and newest. `--describe <file>` rewrites only the summary of an existing fleet from the ships it lists.

The script refuses to replace an existing file without `--force`, since fleets are edited by hand. With `OPENWATERS_TOKEN` set it asks the API with that token, which raises its limit; without one it paces itself under the anonymous limit, about a ship a second.

## YouTube avatars

[scripts/youtube-avatars.mjs](../../scripts/youtube-avatars.mjs) writes each channel's avatar into a fleet whose sections link to YouTube channels, from the YouTube Data API. It changes only the `avatar:` lines, so rerun it to refresh them; YouTube's API terms ask that stored channel data be refreshed at least every 30 days.

```bash
YOUTUBE_API_KEY=... node client/scripts/youtube-avatars.mjs client/app/fleets/youtube/*.yaml
```
