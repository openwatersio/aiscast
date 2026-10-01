// Command places builds places.tsv.gz, the gazetteer the server labels stations from: GeoNames towns of at
// least 5,000 people, with their region and country spelled out. GeoNames is CC BY 4.0. Run it from server/
// when the list needs a refresh:
//
//	go run ./cmd/places
package main

import (
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
)

const base = "https://download.geonames.org/export/dump/"

// skip are populated-place codes that are not towns: neighborhoods, and historical, abandoned, or destroyed
// places. A neighborhood would label a station "Downtown, HI".
var skip = map[string]bool{"PPLX": true, "PPLH": true, "PPLQ": true, "PPLW": true, "PPLCH": true}

func fetch(name string) []byte {
	res, err := http.Get(base + name)
	if err != nil {
		log.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		log.Fatalf("%s: %s", name, res.Status)
	}
	b, err := io.ReadAll(res.Body)
	if err != nil {
		log.Fatal(err)
	}
	return b
}

// lines yields the tab-separated fields of each non-comment line.
func lines(b []byte, f func([]string)) {
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		if l := sc.Text(); l != "" && !strings.HasPrefix(l, "#") {
			f(strings.Split(l, "\t"))
		}
	}
}

func main() {
	countries := map[string]string{}
	lines(fetch("countryInfo.txt"), func(c []string) { countries[c[0]] = c[4] })
	regions := map[string]string{}
	lines(fetch("admin1CodesASCII.txt"), func(c []string) { regions[c[0]] = c[1] })

	zb := fetch("cities5000.zip")
	zr, err := zip.NewReader(bytes.NewReader(zb), int64(len(zb)))
	if err != nil {
		log.Fatal(err)
	}
	f, err := zr.Open("cities5000.txt")
	if err != nil {
		log.Fatal(err)
	}
	cities, err := io.ReadAll(f)
	if err != nil {
		log.Fatal(err)
	}

	var rows []string
	lines(cities, func(c []string) {
		if len(c) < 15 || skip[c[7]] {
			return
		}
		name, cc, admin1, pop := c[1], c[8], c[10], c[14]
		country := countries[cc]
		if country == "" {
			return
		}
		region := regions[cc+"."+admin1]
		// US towns read "Town, ST": GeoNames' US region codes are postal abbreviations. Elsewhere region codes
		// are numbers, so the country is spelled out, which also keeps "CA" from meaning Canada and California.
		var label, area string
		if cc == "US" {
			label, area = name+", "+admin1, region
		} else {
			label, area = name+", "+country, country
			if region != "" {
				area = region + ", " + country
			}
		}
		if area == "" {
			area = country
		}
		rows = append(rows, strings.Join([]string{label, area, pop, c[4], c[5]}, "\t"))
	})
	sort.Strings(rows) // a stable file, so a refresh diffs by what changed

	out, err := os.Create("places.tsv.gz")
	if err != nil {
		log.Fatal(err)
	}
	zw, _ := gzip.NewWriterLevel(out, gzip.BestCompression)
	fmt.Fprintln(zw, "# GeoNames (geonames.org), CC BY 4.0: towns of 5,000+. label\tregion\tpopulation\tlat\tlon")
	for _, r := range rows {
		fmt.Fprintln(zw, r)
	}
	if err := zw.Close(); err != nil {
		log.Fatal(err)
	}
	if err := out.Close(); err != nil {
		log.Fatal(err)
	}
	log.Printf("places.tsv.gz: %d places", len(rows))
}
