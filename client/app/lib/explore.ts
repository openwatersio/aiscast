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
  {
    id: "nbjs",
    name: "No Bullshit Just Sailing",
    crew: "Erik Aanderaa",
    youtube: "https://www.youtube.com/@erikaanderaa",
    boats: [
      {
        name: "Tessie",
        model: "Contessa 35",
        chapter: "Solo North Atlantic sailing",
        source: "https://nbjs.no/",
        mmsi: 257528790,
        identitySource:
          "https://www.vesselfinder.com/vessels/details/257528790",
      },
    ],
  },
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
    id: "florence",
    name: "Sailing Yacht Florence",
    crew: "Matt Humphreys & Amy Cartwright",
    youtube: "https://www.youtube.com/@sailingflorence",
    boats: [
      {
        name: "Florence",
        model: "Oyster Heritage 37",
        chapter: "Circumnavigation & refit",
        source: "https://sailwiththeflo.wordpress.com/florence/",
      },
    ],
  },
  {
    id: "sam-holmes",
    name: "Sam Holmes Sailing",
    crew: "Sam Holmes",
    youtube: "https://www.youtube.com/@samholmessailing",
    boats: [
      {
        name: "Pickled Catfish",
        model: "43-foot Schionning catamaran",
        chapter: "Current boat",
        source: "https://www.patreon.com/posts/tour-of-our-next-125870605",
      },
      {
        name: "Pickled Herring",
        model: "Cape Dory 28",
        chapter: "Previous boat",
        source:
          "https://windpilot.com/blog/blog/portraits/sv-pickled-herring-sam-holmes-us/",
      },
      {
        name: "Swedish Fish",
        model: "Ranger 23",
        chapter: "Earlier boat",
        source:
          "https://windpilot.com/blog/blog/portraits/sv-pickled-herring-sam-holmes-us/",
      },
    ],
  },
  {
    id: "phoenix",
    name: "Sailing With Phoenix",
    crew: "Oliver Widger & Phoenix the cat",
    youtube: "https://www.youtube.com/channel/UCAub01nC6godaiC7iI9kasw",
    boats: [
      {
        name: "Phoenix do Mar",
        model: "Pacific Seacraft 40",
        chapter: "Current boat",
        source: "https://www.youtube.com/watch?v=fASS2iAo5FU",
        mmsi: 368448560,
        identitySource:
          "https://www.vesselfinder.com/vessels/details/368448560",
      },
      {
        name: "Phoenix",
        model: "33-foot cruising sailboat",
        chapter: "Previous boat",
        source:
          "https://www.buzzsprout.com/2321035/episodes/16992754-salty-podcast-58-sailing-oregon-to-hawaii-sailing_with_phoenix-prepares-to-cross-the-pacific",
      },
    ],
  },
  {
    id: "alluring-arctic",
    name: "Alluring Arctic",
    crew: "Juho Karhu & Sohvi Kangasluoma",
    youtube: "https://www.youtube.com/@AlluringArctic",
    boats: [
      {
        name: "Lumi",
        model: "Garcia Nouanni 43/46 aluminum sailboat",
        chapter: "Current boat",
        source: "https://www.alluringarctic.com/about-us",
        mmsi: 230174360,
        identitySource:
          "https://www.myshiptracking.com/vessels/lumi-mmsi-230174360-imo-",
      },
      {
        name: "Sylvia",
        model: "Beneteau Idylle 11.50",
        chapter: "Previous boat",
        source: "https://www.alluringarctic.com/our-story",
      },
    ],
  },
  {
    id: "wind-hippie",
    name: "Wind Hippie Sailing",
    crew: "Holly Martin",
    youtube: "https://www.youtube.com/@WindHippieSailing",
    boats: [
      {
        name: "Gecko",
        model: "Grinde 27",
        chapter: "Solo sailing",
        source: "https://windhippie.com/about-my-boat/",
      },
    ],
  },
  {
    id: "distant-shores",
    name: "Distant Shores",
    crew: "Paul & Sheryl Shard",
    youtube: "https://www.youtube.com/@DistantShoresTV",
    boats: [
      {
        name: "Distant Shores IV",
        model: "Enksail Orion 49 aluminum sailboat",
        chapter: "Fourth Distant Shores boat",
        source:
          "https://www.patreon.com/distantshorestv/posts/new-video-shores-103883084",
        mmsi: 232057925,
        identitySource:
          "https://www.harbourmaps.com/ship/journey/q?mmsi=232057925",
      },
      {
        name: "Distant Shores III",
        model: "Southerly 480",
        chapter: "Previous boat",
        source:
          "https://www.distantshores.ca/boatblog_files/category-distant-shores-iii.php",
      },
      {
        name: "Distant Shores II",
        model: "Southerly 49",
        chapter: "Earlier boat",
        source:
          "https://distantshores.ca/boatblog_files/distant-shores-3-criteria-for-the-around-the-world-sailboat.php",
      },
      {
        name: "Distant Shores",
        model: "Southerly 42",
        chapter: "First Distant Shores boat",
        source:
          "https://distantshores.ca/boatblog_files/distant-shores-3-criteria-for-the-around-the-world-sailboat.php",
      },
      {
        name: "Two-Step",
        model: "Classic 37",
        chapter: "Original boat",
        source:
          "https://distantshores.ca/boatblog_files/distant-shores-3-criteria-for-the-around-the-world-sailboat.php",
      },
    ],
  },
  {
    id: "magic-carpet",
    name: "Sailing Magic Carpet",
    crew: "Maya & Aladino",
    youtube: "https://www.youtube.com/@SailingMagicCarpet",
    boats: [
      {
        name: "Magic Carpet II",
        model: "Cape George 36",
        chapter: "Second boat",
        source: "https://sailingmagiccarpet.com/about/",
      },
      {
        name: "Magic Carpet I",
        model: "Vindö 32 (28 feet)",
        chapter: "Previous boat",
        source: "https://sailingmagiccarpet.com/about/",
      },
    ],
  },
];
