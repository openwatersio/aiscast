import { describe, expect, it } from "vitest";
import { readVisitor, visitorMeta } from "./visitor";

/** A head holding `html`, as far as readVisitor looks: the content of the one meta it asks for. */
const doc = (html: string) =>
  ({
    querySelector: () => {
      const content = /content="([^"]*)"/.exec(html)?.[1];
      return content == null ? null : { getAttribute: () => content };
    },
  }) as unknown as Pick<Document, "querySelector">;

describe("visitor", () => {
  it("reads back the location the Worker writes", () => {
    expect(readVisitor(doc(visitorMeta([10.75, 59.91])))).toEqual([10.75, 59.91]);
  });

  it("is undefined when the Worker could not place the visitor", () => {
    expect(readVisitor(doc(""))).toBeUndefined();
    expect(readVisitor(doc('<meta name="aiscast-visitor" content="10.75,">'))).toBeUndefined();
    expect(readVisitor(doc('<meta name="aiscast-visitor" content="x,y">'))).toBeUndefined();
  });
});
