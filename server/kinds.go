package main

import (
	"regexp"
	"strings"
)

// Kinds a station's messages do not set. A message type names aton (type 21), base (type 4), and sar (type 9), and
// those win. The rest come from what the station says about itself, because the MMSI it sends is often one its maker
// or owner picked: real ships use beacon numbers, and net buoys use ship, aid, and made-up numbers alike.
//
// A distress beacon (AIS-SART, man-overboard, EPIRB-AIS) has a 970, 972, or 974 MMSI and sends class A reports with
// navigation status 14, "AIS-SART active" (IEC 61097-14). The number alone is not enough, and a beacon number whose
// later reports say otherwise is a vessel again.
//
// Gear is a transmitter on fishing gear, in practice a net buoy: a device ITU-R M.2135 keeps off the AIS channels,
// which many send on anyway. ITU-R M.585 gives such devices 979xxxxxx; the rest are known by their static data, as
// Global Fishing Watch knows them: a battery level closing the name, which the buoy rewrites as it drains; a voltage
// in the name; a name that says buoy or net marker; or a 10 m square hull, which no vessel has. All but the battery
// level count only from a station that reports no ship's type, as BUOYSUND, a cargo ship, and BUOY TIME, a yacht,
// do.

// derivedKind is the kind v's own state gives it: sar, gear, or vessel.
func derivedKind(mmsi uint32, v *vessel) string {
	name := strings.TrimSpace(v.Name) // aisstream relays names padded to 20 characters
	switch {
	case isBeaconMMSI(mmsi) && v.NavStatus == 14:
		return "sar"
	case mmsi/1_000_000 == 979:
		return "gear"
	case gearBattery.MatchString(name):
		return "gear"
	case !shipType(v.ShipType) && (gearVoltage.MatchString(name) || gearName.MatchString(name) || v.Length == 10 && v.Beam == 10):
		return "gear"
	}
	return "vessel"
}

// isBeaconMMSI reports an AIS-SART (970), man-overboard (972), or EPIRB-AIS (974) number.
func isBeaconMMSI(mmsi uint32) bool {
	p := mmsi / 1_000_000
	return p == 970 || p == 972 || p == 974
}

// shipType reports an ITU ship and cargo type a vessel would send: 20 to 99. Gear sends none, or a reserved one.
func shipType(t uint8) bool { return t >= 20 && t <= 99 }

var (
	// gearBattery is a battery level closing the name, HSD-NET-88%, which buoys send whatever type they claim.
	gearBattery = regexp.MustCompile(`[0-9]\s*%$`)
	// gearVoltage is a voltage closing the name or inside it: 994163978 3V, BUOY35728-04 7V7, BUOY 7.5V. A ship's
	// name can end in a digit and V, so it counts only as the buoy's name does.
	gearVoltage = regexp.MustCompile(`[0-9]\s*V[0-9]?$|[0-9]\.[0-9]V|@[0-9]+V`)
	// gearName is a name that says what the station is.
	gearName = regexp.MustCompile(`BUOY|BOUY|NET ?MARK|NET ?FISH`)
)

// servedFlag is the flag a station shows: none for gear, whose MID is whatever number its maker or owner chose.
func servedFlag(mmsi uint32, kind string) string {
	if kind == "gear" {
		return ""
	}
	return flagOf(mmsi)
}
