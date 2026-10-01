package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	_ "embed"
	"log"
	"math"
	"slices"
	"strconv"
	"strings"
	"sync"
)

// Coverage labels: each volunteer station is labeled with the town or region nearest the traffic it hears,
// so a station nobody has named still reads as a place. The point is where the traffic is, not where the
// antenna is, and the places are towns of 5,000 or more, so a label never pins a receiver down.

//go:embed places.tsv.gz
var placesGz []byte // built by cmd/places from GeoNames, CC BY 4.0

type place struct {
	label, region string // "Santa Monica, CA" and "California"; "Valencia, Spain" and "Valencia, Spain"
	pop           int
	lat, lon      float64
}

const (
	labelLargestKM = 10  // the most populous town this close wins: "Valencia", not its suburb
	labelNearestKM = 25  // otherwise the nearest town this close: a harbor town, not a bigger one inland
	labelRegionKM  = 100 // otherwise the nearest town's region: remote coasts have no town of 5,000 nearby
	labelMinPoints = 5   // vessels with positions a station needs before its median means anything
)

var places = sync.OnceValue(func() []place {
	zr, err := gzip.NewReader(bytes.NewReader(placesGz))
	if err != nil {
		log.Printf("places: %v", err)
		return nil
	}
	var out []place
	sc := bufio.NewScanner(zr)
	for sc.Scan() {
		f := strings.Split(sc.Text(), "\t")
		if len(f) != 5 || strings.HasPrefix(f[0], "#") {
			continue
		}
		pop, _ := strconv.Atoi(f[2])
		lat, _ := strconv.ParseFloat(f[3], 64)
		lon, _ := strconv.ParseFloat(f[4], 64)
		out = append(out, place{label: f[0], region: f[1], pop: pop, lat: lat, lon: lon})
	}
	return out
})

// distKM is the equirectangular distance, plenty for tens of kilometres.
func distKM(lat1, lon1, lat2, lon2 float64) float64 {
	const rad = math.Pi / 180
	dlon := math.Remainder(lon2-lon1, 360)
	x := dlon * rad * math.Cos((lat1+lat2)/2*rad)
	y := (lat2 - lat1) * rad
	return 6371 * math.Hypot(x, y)
}

// nearLabel names the place for a point: the most populous town within labelLargestKM, else the nearest town
// within labelNearestKM, else the nearest town's region within labelRegionKM, else nothing.
func nearLabel(lat, lon float64) string {
	var largest, nearest *place
	nearestKM := math.Inf(1)
	ps := places()
	for i := range ps {
		p := &ps[i]
		if math.Abs(p.lat-lat) > 1 { // about 111 km: cheap rejection before the trigonometry
			continue
		}
		d := distKM(lat, lon, p.lat, p.lon)
		if d < nearestKM {
			nearest, nearestKM = p, d
		}
		if d <= labelLargestKM && (largest == nil || p.pop > largest.pop) {
			largest = p
		}
	}
	switch {
	case largest != nil:
		return largest.label
	case nearestKM <= labelNearestKM:
		return nearest.label
	case nearestKM <= labelRegionKM:
		return nearest.region
	}
	return ""
}

// medianPoint is the median latitude and longitude of positions, with longitudes taken around the antimeridian
// when they straddle it, so a station in Fiji is not placed in Africa.
func medianPoint(lats, lons []float64) (lat, lon float64) {
	median := func(v []float64) float64 {
		slices.Sort(v)
		if n := len(v); n%2 == 1 {
			return v[n/2]
		} else {
			return (v[n/2-1] + v[n/2]) / 2
		}
	}
	east, west := false, false
	for _, x := range lons {
		east = east || x > 90
		west = west || x < -90
	}
	if east && west {
		for i, x := range lons {
			if x < 0 {
				lons[i] = x + 360
			}
		}
	}
	return median(lats), math.Remainder(median(lons), 360)
}
