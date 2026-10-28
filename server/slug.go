package main

import (
	"database/sql/driver"
	"strconv"
	"strings"

	unorm "golang.org/x/text/unicode/norm"
	"modernc.org/sqlite"
)

// vessel_slug(name) is vesselSlug in SQL, so a query can compare page addresses rather than names, which
// differ by case, padding and punctuation that the address drops. Registered before the store opens.
func init() {
	if err := sqlite.RegisterDeterministicScalarFunction("vessel_slug", 1, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		switch v := args[0].(type) {
		case string:
			return vesselSlug(v), nil
		case []byte:
			return vesselSlug(string(v)), nil
		}
		return "", nil
	}); err != nil {
		panic(err)
	}
}

// The web client names each vessel page /ais/vessels/{mmsi}-{slug} and redirects any other slug to that
// one, so a URL the server builds for a page must carry the client's slug exactly. vesselSlug is the
// client's vesselSlug (client/app/lib/ais.ts) in Go; testdata/vessel-slugs.json holds both to the same
// answers, and each side's tests read it.

// Letters NFD cannot help with: distinct letters rather than an accented base, which stripping marks would drop.
var slugTransliterate = map[rune]string{
	'æ': "ae", 'ø': "o", 'œ': "oe", 'ð': "d", 'đ': "d", 'þ': "th", 'ß': "ss", 'ł': "l", 'ŋ': "n", 'ŧ': "t",
}

// slugMax caps a slug's length, as the client does.
const slugMax = 60

// vesselSlug is the cosmetic half of a vessel page's path: the name lowercased, transliterated, stripped
// of accents, with every run of anything but a-z and 0-9 made one hyphen, and no hyphen at either end.
func vesselSlug(name string) string {
	var lower strings.Builder
	for _, r := range strings.ToLower(name) {
		if t, ok := slugTransliterate[r]; ok {
			lower.WriteString(t)
		} else {
			lower.WriteRune(r)
		}
	}
	var out []byte
	gap := false
	for _, r := range unorm.NFD.String(lower.String()) {
		switch {
		case r >= 0x300 && r <= 0x36f: // combining marks go before anything else is judged, so é is e, not a gap
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			if gap && len(out) > 0 {
				out = append(out, '-')
			}
			gap = false
			out = append(out, byte(r))
		default:
			gap = true
		}
	}
	if len(out) > slugMax {
		out = out[:slugMax]
	}
	return strings.TrimRight(string(out), "-")
}

// vesselPagePath is a vessel page's path on openwaters.io, as the client's vesselPath gives it under /ais/.
func vesselPagePath(mmsi uint32, name string) string {
	path := "/ais/vessels/" + strconv.FormatUint(uint64(mmsi), 10)
	if slug := vesselSlug(name); slug != "" {
		path += "-" + slug
	}
	return path
}
