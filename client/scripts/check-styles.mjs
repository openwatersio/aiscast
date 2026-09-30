// Components style themselves with the design tokens as Tailwind utilities (bg-surface,
// text-fg-muted) and never name a CSS variable, so a theme or token change is made in app.css
// alone. This fails on any var(--…) in a component, in a class name or a style alike.
import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";

const found = [];
const walk = (dir) => {
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const path = join(dir, entry.name);
    if (entry.isDirectory()) walk(path);
    else if (path.endsWith(".tsx")) {
      readFileSync(path, "utf8")
        .split("\n")
        .forEach((line, i) => {
          if (line.includes("var(--")) found.push(`${path}:${i + 1}: ${line.trim()}`);
        });
    }
  }
};
walk("app");

if (found.length) {
  console.error("CSS variables in components; use the token utilities from app.css instead:\n" + found.join("\n"));
  process.exit(1);
}
