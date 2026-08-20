import { describe, expect, it } from "vitest";
import { isAis, isOwnShip, payloadKey, stripTag, tagged, xorChecksum } from "../src/nmea.js";
import { bbox, distanceNm } from "../src/downlink.js";

const VDM = "!AIVDM,1,1,,A,13HOI:0P0000VOHLCnHQKwvL05Ip,0*23";
const TAGGED = "\\s:2573010,c:1787234980*03\\!BSVDM,1,1,,B,13noH:00000H@P@RSPEakGK@0D33,0*43";

describe("nmea helpers", () => {
  it("recognises AIS sentences from any talker, with or without TAG block", () => {
    expect(isAis(VDM)).toBe(true);
    expect(isAis(TAGGED)).toBe(true);
    expect(isAis("!AIVDO,1,1,,A,13HOI:0P0000VOHLCnHQKwvL05Ip,0*23")).toBe(true);
    expect(isAis("$GPRMC,123519,A,4807.038,N,01131.000,E,022.4,084.4,230394,003.1,W*6A")).toBe(false);
    expect(isOwnShip("!AIVDO,1,1,,A,13HOI:0P0000VOHLCnHQKwvL05Ip,0*23")).toBe(true);
    expect(isOwnShip(VDM)).toBe(false);
  });

  it("prepends a checksummed c: TAG block only when missing", () => {
    const t = tagged(VDM, 1787234980123);
    expect(t).toBe(`\\c:1787234980123*${xorChecksum("c:1787234980123")}\\${VDM}`);
    expect(xorChecksum("s:2573010,c:1787234980")).toBe("03"); // Kystverket sample
    expect(tagged(TAGGED, 1)).toBe(TAGGED);
    expect(stripTag(t)).toBe(VDM);
  });

  it("keys the loop guard on payload and channel", () => {
    expect(payloadKey(VDM)).toBe("13HOI:0P0000VOHLCnHQKwvL05Ip/A");
    expect(payloadKey(tagged(VDM, 1))).toBe(payloadKey(VDM));
  });
});

describe("bbox", () => {
  it("is the radius in both axes, wider in longitude at high latitude", () => {
    const [minLat, minLon, maxLat, maxLon] = bbox({ lat: 60, lon: 10, radiusNm: 60 });
    expect(maxLat - minLat).toBeCloseTo(2, 5);
    expect(maxLon - minLon).toBeCloseTo(4, 2);
    expect(distanceNm(60, 10, 61, 10)).toBeCloseTo(60, 0);
  });
});
