import { Activity, RadioTower, type LucideIcon } from "lucide-react";
import { ABOUT, DEVELOPERS } from "./links";

export interface Destination {
  label: string;
  hint: string;
  icon: LucideIcon;
  /** A route in the app. */
  to: string;
}

/**
 * The data the app can browse, listed under search in the home panel. The panel is for
 * looking things up; asking developers and contributors is left to prompts on the pages
 * where it fits.
 */
export const BROWSE: Destination[] = [
  { to: "/stations", label: "Stations", hint: "Who is receiving, and where", icon: RadioTower },
  { to: "/network", label: "Network", hint: "Coverage, sources and delay", icon: Activity },
];

/**
 * The project around the map, in the header: how to build on it and what it is. Both are on
 * the website. The header's Contribute button comes after them.
 */
export const SITE: Array<{ label: string; href: string }> = [
  { label: "Explore", href: "/ais/explore" },
  { label: "Developers", href: DEVELOPERS },
  { label: "About", href: ABOUT },
];
