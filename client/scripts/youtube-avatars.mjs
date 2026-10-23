#!/usr/bin/env node
// Writes each channel's avatar into a fleet whose sections are YouTube channels: for every section
// with a YouTube link, the channel's thumbnail from the YouTube Data API, as the section's
// `avatar:`. Only those lines change, so the rest of the hand-edited file stays as it is, and a
// rerun refreshes them; YouTube's API terms ask that stored channel data be refreshed at least
// every 30 days.
//
//   YOUTUBE_API_KEY=... node client/scripts/youtube-avatars.mjs client/app/fleets/youtube/*.yaml
//
// The key is a YouTube Data API v3 key from a Google Cloud project.

import { readFileSync, writeFileSync } from "node:fs";
import { load } from "js-yaml";

const key = process.env.YOUTUBE_API_KEY;
const files = process.argv.slice(2);
if (!key || !files.length) {
  console.error("usage: YOUTUBE_API_KEY=... youtube-avatars.mjs <fleet.yaml>...");
  process.exit(2);
}

/**
 * The channel a link names, as channels.list takes it: `@handle`, `/channel/UC…`, a legacy
 * `/user/name`, or a custom `/name`, which YouTube carried over as the channel's handle.
 */
function channelQuery(url) {
  const { pathname } = new URL(url);
  const handle = /^\/(@[\w.-]+)/.exec(pathname)?.[1];
  if (handle) return `forHandle=${encodeURIComponent(handle)}`;
  const id = /^\/channel\/(UC[\w-]{22})/.exec(pathname)?.[1];
  if (id) return `id=${id}`;
  const user = /^\/user\/([\w.-]+)/.exec(pathname)?.[1];
  if (user) return `forUsername=${encodeURIComponent(user)}`;
  const custom = /^\/(?:c\/)?([\w.-]+)\/?$/.exec(pathname)?.[1];
  if (custom) return `forHandle=${encodeURIComponent(`@${custom}`)}`;
  return undefined;
}

async function avatar(url) {
  const query = channelQuery(url);
  if (!query) throw new Error(`${url}: not a link to a channel`);
  const res = await fetch(`https://www.googleapis.com/youtube/v3/channels?part=snippet&${query}&key=${key}`);
  if (!res.ok) throw new Error(`${url}: YouTube ${res.status} ${(await res.text()).slice(0, 200)}`);
  const channel = (await res.json()).items?.[0];
  if (!channel) throw new Error(`${url}: no such channel`);
  // 240px, which covers a 40px avatar at any screen density.
  const thumbs = channel.snippet.thumbnails;
  return (thumbs.medium ?? thumbs.high ?? thumbs.default).url;
}

let failed = false;
for (const file of files) {
  let text = readFileSync(file, "utf8");
  const fleet = load(text);
  for (const section of fleet.sections ?? []) {
    const link = section.links?.YouTube;
    if (!link) continue;
    let url;
    try {
      url = await avatar(link);
    } catch (e) {
      console.error(`${file}: ${e.message}`);
      failed = true;
      continue;
    }
    // The section's block runs from its `- title:` line to the next section's.
    // However YAML quoted the title, it reads back as the same text.
    const prefix = "  - title: ";
    const start = text.split("\n").findIndex((l) => l.startsWith(prefix) && load(l.slice(prefix.length)) === section.title);
    if (start < 0) {
      console.error(`${file}: cannot find the section "${section.title}" to write its avatar`);
      failed = true;
      continue;
    }
    const lines = text.split("\n");
    let end = lines.findIndex((l, i) => i > start && l.startsWith("  - "));
    if (end < 0) end = lines.length;
    const at = lines.slice(start, end).findIndex((l) => l.startsWith("    avatar: "));
    if (at >= 0) lines[start + at] = `    avatar: ${url}`;
    else lines.splice(start + 1, 0, `    avatar: ${url}`);
    text = lines.join("\n");
    console.error(`${section.title}: ${url}`);
  }
  writeFileSync(file, text);
}
process.exit(failed ? 1 : 0);
