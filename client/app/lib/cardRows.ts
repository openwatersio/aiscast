/**
 * How a group's cards fall into rows: one wide card, then two rows of pairs, again and again, so a
 * wide card leads every third row. The end bends so no pair is left with one card: a last card on
 * its own goes wide, and where that would put two wide rows together, the wide row before it
 * becomes a pair instead. Each row is the cards it holds, one for a wide row, two for a pair.
 */
export function cardRows<T>(cards: T[]): T[][] {
  const n = cards.length;
  if (n === 0) return [];
  // After the first wide card, blocks of pair, pair, wide.
  const m = n - 1;
  const blocks = Math.floor(m / 5);
  const rest = m % 5;
  const sizes = [1];
  for (let i = 0; i < blocks; i++) sizes.push(2, 2, 1);
  if (rest === 1) {
    // One left over: on its own it would sit wide under a wide row, so it joins that row as a pair.
    if (blocks > 0) sizes[sizes.length - 1] = 2;
    else sizes.push(1);
  } else if (rest === 2) sizes.push(2);
  else if (rest === 3) sizes.push(2, 1);
  else if (rest === 4) sizes.push(2, 2);
  const rows: T[][] = [];
  let at = 0;
  for (const size of sizes) {
    rows.push(cards.slice(at, at + size));
    at += size;
  }
  return rows;
}
