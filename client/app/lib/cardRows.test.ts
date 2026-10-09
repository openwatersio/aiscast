import { expect, it } from "vitest";
import { cardRows } from "./cardRows";

const shape = (n: number) => cardRows(Array.from({ length: n }, (_, i) => i)).map((row) => (row.length === 1 ? "W" : "HH")).join(",");

it("leads with a wide card, puts one on every third row, and never leaves a pair with one card", () => {
  expect(shape(0)).toBe("");
  expect(shape(1)).toBe("W");
  expect(shape(2)).toBe("W,W");
  expect(shape(3)).toBe("W,HH");
  expect(shape(4)).toBe("W,HH,W");
  expect(shape(5)).toBe("W,HH,HH");
  expect(shape(6)).toBe("W,HH,HH,W");
  expect(shape(7)).toBe("W,HH,HH,HH");
  expect(shape(8)).toBe("W,HH,HH,W,HH");
  expect(shape(9)).toBe("W,HH,HH,W,HH,W");
  expect(shape(10)).toBe("W,HH,HH,W,HH,HH");
  expect(shape(12)).toBe("W,HH,HH,W,HH,HH,HH");
  for (let n = 1; n <= 40; n++) {
    const rows = cardRows(Array.from({ length: n }, (_, i) => i));
    expect(rows.flat(), `${n}`).toEqual(Array.from({ length: n }, (_, i) => i));
    expect(rows.slice(1).some((r, i) => r.length === 1 && rows[i]!.length === 1 && i > 0), `${n}: two wide rows together`).toBe(false);
  }
});
