import { redirect } from "react-router";

// Where fleets were first published, as standalone pages, kept as permanent redirects for links to them.
const MOVED: Record<string, string> = {
  "tech-yachts": "/fleets/superyachts/tech-billionaires",
  youtube: "/fleets/youtube",
  "youtube/sailors": "/fleets/youtube/sailors",
  "youtube/cruisers": "/fleets/youtube/cruisers",
};

function toFleets(params: { "*"?: string }): never {
  const from = (params["*"] ?? "").replace(/\/+$/, "");
  // Own keys only: `constructor` and the like are on every object.
  throw redirect(Object.hasOwn(MOVED, from) ? MOVED[from]! : "/fleets", 301);
}

export const loader = ({ params }: { params: { "*"?: string } }) => toFleets(params);
export const clientLoader = ({ params }: { params: { "*"?: string } }) => toFleets(params);
