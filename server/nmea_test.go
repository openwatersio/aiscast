package main

import "testing"

func TestOwnShipToVDM(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"!AIVDO,1,1,,A,13HOI:0P0000VOHLCnHQKwvL05Ip,0*21", "!AIVDM,1,1,,A,13HOI:0P0000VOHLCnHQKwvL05Ip,0*23"},
		{"!BSVDO,1,1,,B,13noH:00000H@P@RSPEakGK@0D33,0*41", "!BSVDM,1,1,,B,13noH:00000H@P@RSPEakGK@0D33,0*43"}, // any talker
		// A TAG block carries its own checksum and stays as it is.
		{`\s:self,c:1787234990*2C\!AIVDO,1,1,,A,13HOI:0P0000VOHLCnHQKwvL05Ip,0*21`, `\s:self,c:1787234990*2C\!AIVDM,1,1,,A,13HOI:0P0000VOHLCnHQKwvL05Ip,0*23`},
		{"!AIVDM,1,1,,A,13HOI:0P0000VOHLCnHQKwvL05Ip,0*23", "!AIVDM,1,1,,A,13HOI:0P0000VOHLCnHQKwvL05Ip,0*23"}, // not own ship
		{"$GPGLL,4916.45,N,12311.12,W,225444,A,*1D", "$GPGLL,4916.45,N,12311.12,W,225444,A,*1D"},               // not AIS
		{"!AIVDO", "!AIVDO"}, // no field separator: left alone rather than indexed past the end
		{"", ""},
	} {
		if got := ownShipToVDM(c.in); got != c.want {
			t.Errorf("ownShipToVDM(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
