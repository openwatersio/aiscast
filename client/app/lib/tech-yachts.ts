import type { BoatVersion } from "./explore";

export const TECH_YACHTS_PATH = "/explore/tech-yachts";

export interface TechYacht extends BoatVersion {
  id: string;
  person: string;
  connection: string;
  description: string;
  ownershipSource: string;
}

export const techYachts: { id: string; title: string; boats: TechYacht[] }[] = [
  {
    id: "sailing",
    title: "Sailing",
    boats: [
      {
        id: "koru",
        name: "Koru",
        person: "Jeff Bezos",
        connection: "Amazon founder",
        model: "125 m / 410 ft · Oceanco · Three-masted schooner",
        chapter: "Reported owner",
        description:
          "Delivered in 2023. Oceanco lists her length as 125 metres; many published accounts quote 127 metres.",
        source: "https://www.oceancoyacht.com/fleet/koru/",
        ownershipSource: "https://en.wikipedia.org/wiki/Koru_(yacht)",
        mmsi: 319225400,
        identitySource: "https://www.vesselfinder.com/vessels/details/9857298",
      },
      {
        id: "athena",
        name: "Athena",
        person: "Jim Clark",
        connection: "Netscape co-founder",
        model: "90 m / 295 ft · Royal Huisman · Three-masted gaff schooner",
        chapter: "Commissioned by",
        description:
          "Delivered in 2004, combining a classic schooner silhouette with modern construction.",
        source: "https://www.royalhuisman.com/en/yachts/athena/",
        ownershipSource:
          "https://www.royalhuisman.com/en/athena-inside-the-royal-huisman-flagship-yacht/",
        mmsi: 319012000,
        identitySource: "https://www.hafen-hamburg.de/de/schiffe/athena/",
      },
      {
        id: "eos",
        name: "Eos",
        person: "Barry Diller & Diane von Furstenberg",
        connection: "IAC / Expedia and fashion",
        model: "93 m / 305 ft · Lürssen · Three-masted schooner",
        chapter: "Reported owners",
        description:
          "Delivered in 2006. Lürssen's rare sailing build remains one of the largest private sailing yachts.",
        source: "https://www.lurssen.com/en/new-build/yachts/eos/",
        ownershipSource: "https://www.yachtbuyer.com/en-gb/fleet/eos-304-lurssen",
        mmsi: 319087000,
        identitySource:
          "https://www.aisfriends.com/vessels/EOS/9377456/319087000/95672",
      },
      {
        id: "ran-vii",
        name: "Rán VII",
        person: "Niklas & Catherine Zennström",
        connection: "Skype co-founder and Rán Racing",
        model: "40 ft / 12.2 m class · Carkeek Design · Fast40+ racing yacht",
        chapter: "Commissioned by",
        description:
          "Launched in 2018 for competitive racing, with electric auxiliary propulsion. She races under sail.",
        source:
          "https://www.sail-world.com/news/204562/Carkeeks-R%C3%A1n-Fast40-promises-electric-partnership",
        ownershipSource: "https://www.carkeekdesignpartners.com/?p=1339",
      },
    ],
  },
  {
    id: "motor",
    title: "Motor",
    boats: [
      {
        id: "launchpad",
        name: "Launchpad",
        person: "Mark Zuckerberg",
        connection: "Facebook / Meta co-founder",
        model: "118 m / 387 ft · Feadship · Motor yacht",
        chapter: "Reported owner",
        description:
          "Delivered in 2024, with exterior design by Espen Øino and interiors by Zuretti.",
        source: "https://feadship.nl/fleet/launchpad",
        ownershipSource:
          "https://www.yachtbuyer.com/en-gb/fleet/launchpad-387-feadship",
        mmsi: 538072122,
        identitySource:
          "https://www.vesselfinder.com/nl/vessels/details/9857511",
      },
      {
        id: "dragonfly",
        name: "Dragonfly",
        person: "Sergey Brin",
        connection: "Google co-founder",
        model: "142 m / 466 ft · Lürssen · Motor yacht",
        chapter: "Reported owner",
        description:
          "The 2024 Lürssen build, formerly Project Alibaba, uses diesel-electric propulsion. This is a different hull from Brin's earlier 73-metre Dragonfly.",
        source: "https://www.lurssen.com/en/new-build/yachts/dragonfly/",
        ownershipSource: "https://www.superyachtfan.com/yacht/dragonfly/",
        mmsi: 319296900,
        identitySource: "https://www.vesselfinder.com/vessels/details/9907196",
      },
      {
        id: "ahpo",
        name: "Ahpo",
        person: "Dmitry Bukhman",
        connection: "Playrix co-founder",
        model: "115 m / 378 ft · Lürssen · Motor yacht",
        chapter: "Reported owner",
        description:
          "Delivered in 2021 for Michael Lee-Chin, then sold to Patrick Dovigi. Public reports associate her with Bukhman following a later sale.",
        source: "https://www.lurssen.com/en/new-build/yachts/ahpo/",
        ownershipSource: "https://en.wikipedia.org/wiki/Ahpo",
        mmsi: 538071653,
        identitySource: "https://www.vesselfinder.com/vessels/details/9855276",
      },
      {
        id: "rising-sun",
        name: "Rising Sun",
        person: "Larry Ellison",
        connection: "Oracle co-founder",
        model: "138 m / 453 ft · Lürssen · Motor yacht",
        chapter: "Former owner",
        description:
          "Built for Ellison in 2004. David Geffen bought a half share in 2006 and the remaining share in 2010.",
        source: "https://www.lurssen.com/en/new-build/yachts/rising-sun/",
        ownershipSource:
          "https://boattest.com/article/20-rising-sun-top-largest-yachts-world",
        mmsi: 319011000,
        identitySource: "https://www.vesselfinder.com/vessels/details/8982307",
      },
      {
        id: "norn",
        name: "Norn",
        person: "Charles Simonyi",
        connection: "Microsoft software pioneer",
        model: "90 m / 295 ft · Lürssen · Motor yacht",
        chapter: "Reported owner",
        description:
          "Delivered in 2023, with angular styling by Espen Øino and a pool floor that lifts to become a dance floor.",
        source: "https://www.lurssen.com/en/new-build/yachts/norn/",
        ownershipSource: "https://en.wikipedia.org/wiki/Charles_Simonyi",
        mmsi: 319261700,
        identitySource:
          "https://www.marineradar.com/vessel/mmsi-319261700/norn",
      },
      {
        id: "skat",
        name: "Skat",
        person: "Charles Simonyi",
        connection: "Microsoft software pioneer",
        model: "71 m / 233 ft · Lürssen · Motor yacht",
        chapter: "Former owner",
        description:
          "Delivered in 2002 with a distinctive grey, angular exterior. Simonyi sold her in 2021 before Norn arrived.",
        source: "https://www.lurssen.com/en/new-build/yachts/skat/",
        ownershipSource: "https://en.wikipedia.org/wiki/Charles_Simonyi",
        mmsi: 319741000,
        identitySource: "https://www.vesselfinder.com/vessels/details/1007287",
      },
      {
        id: "senses",
        name: "Senses",
        person: "Larry Page",
        connection: "Google co-founder",
        model: "59.22 m / 194 ft · Fr. Schweers · Explorer yacht",
        chapter: "Former owner",
        description:
          "Built in 1999. Page bought her in 2011 and later sold her; her AIS position follows the vessel through changes of ownership.",
        source: "https://iyc.com/charter/senses/",
        ownershipSource: "https://www.superyachtfan.com/yacht/senses/owner/",
        mmsi: 319833000,
        identitySource: "https://www.vesselfinder.com/vessels/details/1006673",
      },
      {
        id: "whisper",
        name: "Whisper",
        person: "Eric Schmidt",
        connection: "Former Google CEO",
        model: "95.2 m / 312 ft · Lürssen · Motor yacht",
        chapter: "Reported owner",
        description:
          "Delivered as Kismet in 2014 and renamed after her 2023 sale to Schmidt. Listed for sale again in 2026.",
        source: "https://www.vesselfinder.com/vessels/details/1012000",
        ownershipSource:
          "https://luxurylaunches.com/transport/whisper-superyacht-sale-08172026.php",
        mmsi: 538072792,
        identitySource: "https://www.vesselfinder.com/vessels/details/1012000",
      },
      {
        id: "bliss",
        name: "Bliss",
        person: "Evan Spiegel",
        connection: "Snap co-founder",
        model: "94.75 m / 311 ft · Feadship · Motor yacht",
        chapter: "Reported owner",
        description:
          "Delivered in 2021, with hybrid propulsion and polar certification for remote cruising.",
        source: "https://feadship.nl/fleet/bliss",
        ownershipSource:
          "https://www.seapixonline.com/nsphoto.php?cat=&catn=&hit=1224&mid=0&pid=13167&tot=11711&typ=&wds=",
        mmsi: 538071599,
        identitySource:
          "https://www.shipxplorer.com/data/vessels/bliss-IMO-9835707-MMSI-538071599",
      },
      {
        id: "leviathan",
        name: "Leviathan",
        person: "Gabe Newell",
        connection: "Valve co-founder",
        model: "111 m / 364 ft · Oceanco · Explorer yacht",
        chapter: "Commissioned by",
        description:
          "Delivered in 2025 with diesel-electric propulsion, a dive center, laboratory and hospital. Newell also acquired Oceanco in August 2025.",
        source: "https://www.oceancoyacht.com/fleet/leviathan/",
        ownershipSource:
          "https://swzmaritime.nl/news/2025/11/24/oceanco-delivers-111-metre-yacht-to-owner-newell/",
        mmsi: 319324100,
        identitySource: "https://www.vesselfinder.com/vessels/details/9921491",
      },
      {
        id: "rocinante",
        name: "Rocinante",
        person: "Gabe Newell",
        connection: "Valve co-founder",
        model: "78.4 m / 257 ft · Lürssen · Motor yacht",
        chapter: "Reported owner",
        description:
          "Delivered in 2008 as Madsummer, before taking the names TV and Rocinante.",
        source: "https://www.lurssen.com/en/new-build/yachts/rocinante/",
        ownershipSource: "https://www.superyachtfan.com/es/yacht/rocinante/",
        mmsi: 319840000,
        identitySource: "https://www.vesselfinder.com/vessels/details/1009699",
      },
      {
        id: "octopus",
        name: "Octopus",
        person: "Paul Allen",
        connection: "Late Microsoft co-founder",
        model: "126.2 m / 414 ft · Lürssen · Explorer yacht",
        chapter: "Former owner",
        description:
          "Delivered in 2003 for remote cruising and research. Sold in 2021 after Allen's death.",
        source: "https://www.lurssen.com/en/new-build/yachts/octopus/",
        ownershipSource:
          "https://www.geekwire.com/2022/tatoosh-a-superyacht-owned-by-paul-allen-is-sold-after-being-listed-for-90m/",
        mmsi: 319866000,
        identitySource: "https://www.vesselfinder.com/vessels/details/1007213",
      },
      {
        id: "tatoosh",
        name: "Tatoosh",
        person: "Paul Allen",
        connection: "Late Microsoft co-founder",
        model: "92.4 m / 303 ft · Nobiskrug · Motor yacht",
        chapter: "Former owner",
        description:
          "Built in 2000 and bought by Allen in 2001. Sold by his estate in 2022.",
        source: "https://en.wikipedia.org/wiki/Tatoosh_(yacht)",
        ownershipSource:
          "https://www.geekwire.com/2022/tatoosh-a-superyacht-owned-by-paul-allen-is-sold-after-being-listed-for-90m/",
        mmsi: 319801000,
        identitySource: "https://www.vesselfinder.com/vessels/details/1006336",
      },
      {
        id: "venus",
        name: "Venus",
        person: "Steve Jobs",
        connection: "Late Apple co-founder",
        model: "78.2 m / 257 ft · Feadship · Motor yacht",
        chapter: "Commissioned by",
        description:
          "Launched in 2012 after Jobs' death, with Philippe Starck's glass-rich, minimalist design. Owned by Laurene Powell Jobs.",
        source: "https://feadship.nl/fleet/venus",
        ownershipSource: "https://en.wikipedia.org/wiki/Venus_(yacht)",
        mmsi: 319327000,
        identitySource: "https://www.vesselfinder.com/vessels/details/1011836",
      },
    ],
  },
];
