import { describe, expect, it } from "vitest";
import { particularsFacts } from "./particulars";

const present = (facts: Array<[string, string | undefined]>) => facts.filter(([, v]) => v != null);

describe("particularsFacts", () => {
  it("lists what the sources hold, in units", () => {
    expect(
      present(
        particularsFacts({
          ship_type: "cruise ship",
          builder: "Meyer Werft",
          yard_number: "677",
          year_built: 2010,
          gross_tonnage: 121878,
          deadweight: 9500,
          length: 317.2,
          beam: 36.8,
          draught: 8.62,
          registry: "Malta",
          home_port: "Valletta",
          operator: "Celebrity Cruises",
        }),
      ),
    ).toEqual([
      ["Type", "Cruise ship"],
      ["Built", "2010"],
      ["Builder", "Meyer Werft"],
      ["Yard number", "677"],
      ["Gross tonnage", "121,878"],
      ["Deadweight", "9,500 t"],
      ["Length", "317.2 m"],
      ["Beam", "36.8 m"],
      ["Design draught", "8.62 m"],
      ["Registry", "Malta"],
      ["Home port", "Valletta"],
      ["Operator", "Celebrity Cruises"],
    ]);
  });

  it("joins former names oldest first", () => {
    expect(present(particularsFacts({ former_names: ["Thekla", "Heimar"] }))).toEqual([["Former names", "Thekla, Heimar"]]);
  });

  const kensington = {
    registered_name: "MAERSK KENSINGTON",
    identification: "1257726",
    service: "Freight Ship",
    status: "Active",
    year_built: 2007,
    length: 286.88,
    beam: 39.99,
    depth: 20.3,
    gross_tonnage: 74642,
    net_tonnage: 44243,
    tonnage_measure: "Convention" as const,
    registry: "United States",
  };

  it("lists the documented particulars, leaving out a name AIS already shows", () => {
    expect(present(particularsFacts(kensington, "MAERSK KENSINGTON"))).toEqual([
      ["Official number", "1257726"],
      ["Service", "Freight Ship"],
      ["Status", "Active"],
      ["Built", "2007"],
      ["Gross tonnage", "74,642"],
      ["Net tonnage", "44,243"],
      ["Length", "286.88 m"],
      ["Beam", "39.99 m"],
      ["Depth", "20.3 m"],
      ["Registry", "United States"],
    ]);
  });

  it("shows the documented name when AIS abbreviates it", () => {
    const facts = particularsFacts({ ...kensington, registered_name: "GOVERNOR THOMAS H. KEAN" }, "GOV THOMAS H KEAN");
    expect(facts[0]).toEqual(["Documented as", "GOVERNOR THOMAS H. KEAN"]);
    expect(particularsFacts({ ...kensington, registered_name: "THOMAS D. WITTE" }, "THOMAS D WITTE")[0]?.[1]).toBeUndefined();
  });

  it("says which measure tonnage outside the Convention is", () => {
    const facts = new Map(particularsFacts({ ...kensington, gross_tonnage: 192, net_tonnage: 130, tonnage_measure: "Regulatory" }));
    expect(facts.get("Gross tonnage")).toBe("192 (regulatory)");
    expect(facts.get("Net tonnage")).toBe("130 (regulatory)");
  });

  it("annotates only the tonnages the flag state supplied", () => {
    const facts = new Map(
      particularsFacts(
        { gross_tonnage: 121878, net_tonnage: 130, tonnage_measure: "Regulatory" },
        undefined,
        { gross_tonnage: "wikidata", net_tonnage: "uscg" },
      ),
    );
    expect(facts.get("Gross tonnage")).toBe("121,878");
    expect(facts.get("Net tonnage")).toBe("130 (regulatory)");
  });

  it("names a state registration as such", () => {
    expect(particularsFacts({ ...kensington, identification: "FL8656LB" })[1]).toEqual(["Registration", "FL8656LB"]);
  });
});
