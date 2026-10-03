export const EXPLORE_PATH = "/explore";
export const SAILORS_PATH = `${EXPLORE_PATH}/youtube/sailors`;

export interface BoatVersion {
  name: string;
  model: string;
  chapter: string;
  source?: string;
  mmsi?: number;
  identitySource?: string;
}

export interface SailingChannel {
  id: string;
  name: string;
  crew: string;
  youtube: string;
  boats: BoatVersion[];
}

export const sailingChannels: SailingChannel[] = [
  {
    id: "la-vagabonde",
    name: "Sailing La Vagabonde",
    crew: "Riley Whitelum & Elayna Carausu",
    youtube: "https://www.youtube.com/@SailingLaVagabonde",
    boats: [
      {
        name: "La Vagabonde III",
        model: "Rapido 60 trimaran",
        chapter: "Third boat",
        source: "https://www.sailinglavagabonde.org/our-trimaran",
        mmsi: 268233302,
        identitySource:
          "https://www.myshiptracking.com/vessels/la-vagabond-iii-mmsi-268233302-imo-",
      },
      {
        name: "La Vagabonde II",
        model: "Outremer 45 catamaran",
        chapter: "Previous boat",
        source: "https://www.sailinglavagabonde.org/",
      },
      {
        name: "La Vagabonde I",
        model: "Beneteau Cyclades 43.4",
        chapter: "First boat",
        source: "https://www.sailinglavagabonde.org/",
      },
    ],
  },
  {
    id: "delos",
    name: "SV Delos",
    crew: "Brian Trautman & Karin",
    youtube: "https://www.youtube.com/@svdelos",
    boats: [
      {
        name: "Delos 2.0",
        model: "Delos Explorer 53 aluminum catamaran",
        chapter: "New build",
        source: "https://svdelos.com/delos2/",
      },
      {
        name: "Delos",
        model: "Amel Super Maramu 53",
        chapter: "Original boat",
        source: "https://svdelos.com/frequently-asked-questions/",
      },
    ],
  },
  {
    id: "uma",
    name: "Sailing Uma",
    crew: "Dan Deckert & Kika Mevs",
    youtube: "https://www.youtube.com/@SailingUma",
    boats: [
      {
        name: "Uma",
        model: "Pearson 36, converted to electric",
        chapter: "Electric refit",
        source: "https://sailinguma.com/",
        mmsi: 316039039,
        identitySource:
          "https://www.vesseltracker.com/en/Ships/Uma-I2338997.html",
      },
    ],
  },
  {
    id: "parlay-revival",
    name: "Sailing Parlay Revival",
    crew: "Colin MacRae & crew",
    youtube: "https://www.youtube.com/@ParlayRevival",
    boats: [
      {
        name: "Parlay",
        model: "Lagoon 450 catamaran",
        chapter: "Hurricane recovery & rebuild",
        source: "https://parlayrevival.com/",
        mmsi: 368115250,
        identitySource:
          "https://www.harbourmaps.com/ship/journey/q?mmsi=368115250",
      },
    ],
  },
  {
    id: "wynns",
    name: "Gone With The Wynns",
    crew: "Jason & Nikki Wynn",
    youtube: "https://www.youtube.com/@gonewiththewynns",
    boats: [
      {
        name: "Undra",
        model: "50-foot aluminum explorer yacht",
        chapter: "Third boat",
        source: "https://www.gonewiththewynns.com/explorer-yacht-undra/",
        mmsi: 368478440,
        identitySource: "https://www.aiscatcher.org/ship/details/368478440",
      },
      {
        name: "Curiosity²",
        model: "HH44 sailing catamaran",
        chapter: "Previous boat",
        source: "https://www.gonewiththewynns.com/curiosity-sailboat/",
      },
      {
        name: "Curiosity",
        model: "Leopard 43 catamaran",
        chapter: "First boat",
        source:
          "https://www.gonewiththewynns.com/sweet-life-sailing-dream-boat/",
      },
    ],
  },
  {
    id: "zatara",
    name: "Sailing Zatara",
    crew: "Keith & Renee Whitaker and family",
    youtube: "https://www.youtube.com/@SailingZatara",
    boats: [
      {
        name: "Zatara",
        model: "Privilege 585 catamaran",
        chapter: "Second boat",
        source: "https://sailingzatara.com/the-boat",
      },
      {
        name: "Zatara",
        model: "Beneteau Oceanis 55",
        chapter: "Previous boat",
        source:
          "https://www.dbyboatsales.com.au/?bdbrochure=beneteau-oceanis-55",
      },
    ],
  },
  {
    id: "doodles",
    name: "Sailing Doodles",
    crew: "Bobby White & crew",
    youtube: "https://www.youtube.com/@SailingDoodles",
    boats: [
      {
        name: "Maverick",
        model: "Island Spirit 525E electric catamaran",
        chapter: "Season 13",
        source: "https://sailingdoodles.com/",
      },
      {
        name: "Puss Puss",
        model: "Vessel identity unconfirmed",
        chapter: "Suggested boat",
      },
    ],
  },
  {
    id: "mj-sailing",
    name: "MJ Sailing",
    crew: "Matt & Jessica Johnson",
    youtube: "https://www.youtube.com/@MJSailing",
    boats: [
      {
        name: "Catamaran build",
        model: "Max Cruise 42",
        chapter: "New build",
        source:
          "https://www.totalboat.com/blogs/totalboat/meet-mj-sailing-barrier-coating-and-boat-building",
      },
      {
        name: "Elements of Life",
        model: "Custom aluminum Trisalu 37",
        chapter: "Previous boat",
        source:
          "https://www.mjsailing.com/rebuilding-elements/elements-for-sale/",
      },
      {
        name: "Serendipity",
        model: "Sabre 34",
        chapter: "Earlier boat",
        source: "https://sailing-tellus.com/member/mj-sailing",
      },
    ],
  },
  {
    id: "tally-ho",
    name: "Sampson Boat Co.",
    crew: "Leo Goolden & crew",
    youtube: "https://www.youtube.com/@SampsonBoatCo",
    boats: [
      {
        name: "Tally Ho",
        model: "1910 Albert Strange gaff cutter",
        chapter: "Restored & sailing",
        source: "https://www.yachttallyho.com/",
        mmsi: 235093681,
        identitySource:
          "https://www.marineradar.com/vessel/mmsi-235093681/tally-ho",
      },
    ],
  },
];
