import { describe, expect, it } from "vitest";
import { interpolateAt, speedAt, splitTrack, trackGap, trackThenStream } from "./ais";
import { trackBreaks } from "./useTrack";

const hour = 3600e3;

describe("trackBreaks", () => {
  // A simplified track: a dock stay collapsed to its two ends, hours apart while the vessel was
  // heard throughout, then a real silence the server names.
  const times = [0, 6 * hour, 6.2 * hour, 6.4 * hour, 10 * hour, 10.1 * hour];
  const coords: Array<[number, number]> = times.map((_, i) => [i, i]);
  const sog = [0, 0, 6, 6, 5, 5];

  it("takes the server's breaks, so a collapsed stay is one line", () => {
    const breaks = trackBreaks(times, [4], 0);
    const unheard = (i: number) => breaks.has(i);
    expect([...breaks]).toEqual([4]);
    expect(splitTrack(coords, unheard)).toEqual([coords.slice(0, 4), coords.slice(4)]);
    expect(speedAt({ times, sog }, 3 * hour, unheard)).toBe(0);
    expect(speedAt({ times, sog }, 8 * hour, unheard)).toBeUndefined();
    expect(interpolateAt(coords, times, 3 * hour, unheard)?.inGap).toBe(false);
    expect(interpolateAt(coords, times, 8 * hour, unheard)?.inGap).toBe(true);
  });

  it("finds the silences itself from a server that sends no breaks", () => {
    expect([...trackBreaks(times, undefined, 0)]).toEqual([1, 4]);
    // A track thinned to hours lets an hour's spacing pass.
    expect([...trackBreaks([0, hour, 2 * hour], undefined, 3600)]).toEqual([]);
  });

  it("reads an empty list as a track heard throughout", () => {
    expect([...trackBreaks(times, [], 0)]).toEqual([]);
  });
});

describe("trackThenStream", () => {
  it("keeps the track's own breaks, and judges the stream's positions after it by the track's gap", () => {
    // Three positions thinned to hours, then two from the stream.
    const times = [0, 6 * hour, 7 * hour, 7.9 * hour, 9 * hour];
    const coords: Array<[number, number]> = times.map((_, i) => [i, i]);
    const gap = trackGap(hour);
    // Hours between the track's own positions are no break; 54 minutes into the stream is
    // within a thinned step; more than 2.5 hours later in the stream is a silence.
    expect(splitTrack(coords, trackThenStream(3, new Set(), times, gap))).toEqual([coords]);
    expect(splitTrack(coords, trackThenStream(3, new Set([1]), times, gap))).toEqual([coords.slice(1)]);
    const later = [...times.slice(0, 4), 12 * hour];
    expect(splitTrack(coords, trackThenStream(3, new Set(), later, gap))).toEqual([coords.slice(0, 4)]);
  });

  it("falls back to silences when the track names no breaks", () => {
    const times = [0, 2 * hour];
    expect(trackThenStream(2, undefined, times)(1)).toBe(true);
  });
});

describe("speedAt after the last position", () => {
  it("keeps the speed for a thinned step after it", () => {
    const track = { times: [0, hour], sog: [5, 6] };
    expect(speedAt(track, hour + 50 * 60e3, undefined, trackGap(hour))).toBe(6);
    expect(speedAt(track, hour + 50 * 60e3)).toBeUndefined();
  });
});
