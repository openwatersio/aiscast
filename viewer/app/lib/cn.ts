import { clsx, type ClassValue } from "clsx";
import { extendTailwindMerge } from "tailwind-merge";

// The type scale in app.css. Without it tailwind-merge reads text-title as a colour, and a
// later text-fg would silently drop the size.
const twMerge = extendTailwindMerge({
  extend: { theme: { text: ["large-title", "title", "headline", "body", "subhead", "footnote", "caption"] } },
});

/** Class names, joined, with later Tailwind utilities winning over earlier ones they conflict with. */
export function cn(...values: ClassValue[]): string {
  return twMerge(clsx(values));
}
