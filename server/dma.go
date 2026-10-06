package main

// DMA: the Danish Maritime Authority's AIS archive, every report its shore network heard in Danish waters, the
// Skagerrak, the Kattegat, and the western Baltic, stamped to the second, in a public S3 bucket, three days
// behind. Published under the Danish public sector information act, on the condition that it is not combined so
// that private individuals become identifiable. Each day is a zipped CSV, and ClickHouse reads the CSV inside the
// zip itself. Coordinates have six decimal places, which round back to the exact wire value.

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// dmaBucket is the archive's bucket, path-style: its name has dots, which its TLS certificate does not cover as a
// virtual host. Listing and reading it are anonymous.
var dmaBucket = &s3Client{endpoint: "https://s3.eu-central-1.amazonaws.com", bucket: "aisdata.ais.dk"}

// dmaKey matches a zip in the bucket: one day at the root from 2025-02-27 (aisdk-2025-02-27.zip), one day under its
// year from 2024-03 (2024/aisdk-2024-03-01.zip), and one month under its year before that, with a CSV a day inside
// (2018/aisdk-2018-01.zip; 2017-02 to 2017-06 are 2017/all_sources_2017-02.zip).
var dmaKey = regexp.MustCompile(`^(?:\d{4}/)?(?:aisdk|all_sources)[-_](\d{4}-\d{2})(-\d{2})?\.zip$`)

// dmaColumns are the CSV's columns that a reception or vessel_statics keeps, by their header names. The header
// names 22 columns before 2019 and 26 after, the last four the antenna's offsets; the read skips the ones this
// does not name, so both read the same.
const dmaColumns = "`# Timestamp` String, `Type of mobile` String, MMSI String, Latitude Nullable(Float64), Longitude Nullable(Float64), " +
	"`Navigational status` String, SOG Nullable(Float64), COG Nullable(Float64), Heading Nullable(Float64), IMO String, Callsign String, " +
	"Name String, `Ship type` String, Width Nullable(Float64), Length Nullable(Float64), Draught Nullable(Float64), Destination String"

// dma is the source, loading from from on. DMA turns it on.
func dma(from time.Time) historySource {
	return historySource{name: "dma", from: from, read: dmaRead, list: dmaList}
}

// dmaRead reads one day's CSV out of its zip; the file's url names both.
func dmaRead(f historyFile) string {
	return dmaSelect("s3(" + sqlString(f.url) + ", NOSIGN, 'CSVWithNames', " + sqlString(dmaColumns) + ")")
}

// dmaSelect maps DMA's rows from a table function, in the server's encodings. Tests read from format() instead of
// S3. Only class A and B transponders are vessels; base stations, aids to navigation, and SAR aircraft get MMSI 0,
// so the loader counts them read and keeps none. Timestamps are day-first and UTC. DMA writes a missing position as
// latitude 91 and longitude 0, which latitude alone leaves unplaced, and "Unknown" as empty text. Navigational
// status and ship type come as words; a ship type names its AIS category, so it maps to the category's first code,
// and Cargo is 70 whatever its hazard digit.
func dmaSelect(from string) string {
	return "SELECT if(`Type of mobile` IN ('Class A', 'Class B'), toUInt32OrZero(MMSI), 0) AS mmsi, " +
		"toDateTime64(parseDateTimeOrZero(`# Timestamp`, '%d/%m/%Y %H:%i:%s', 'UTC'), 3, 'UTC') AS ts, " + `
		toInt32(round(coalesce(Latitude, 91) * 600000)) AS lat6, toInt32(round(coalesce(Longitude, 181) * 600000)) AS lon6,
		if(SOG IS NULL OR SOG < 0 OR SOG >= 102.3, 1023, toUInt16(round(SOG * 10))) AS sog10,
		if(COG IS NULL OR COG < 0 OR COG >= 360, 3600, toUInt16(round(COG * 10))) AS cog10,
		if(Heading IS NULL OR Heading < 0 OR Heading >= 360, 511, toUInt16(Heading)) AS heading,
		toUInt8(transform(` + "`Navigational status`" + `, [` + dmaWords(dmaNavStatus) + `], [` + dmaCodes(dmaNavStatus) + `], 15)) AS navstat,
		if(Name = 'Unknown', '', Name) AS name, if(Callsign = 'Unknown', '', Callsign) AS callsign, toUInt32OrZero(IMO) AS imo,
		toUInt8(transform(` + "`Ship type`" + `, [` + dmaWords(dmaShipType) + `], [` + dmaCodes(dmaShipType) + `], 0)) AS ship_type,
		if(Length IS NULL OR Length <= 0 OR Length > 1000, 0, toUInt16(round(Length))) AS length,
		if(Width IS NULL OR Width <= 0 OR Width > 1000, 0, toUInt16(round(Width))) AS beam,
		if(Draught IS NULL OR Draught <= 0 OR Draught > 50, 0, toUInt16(round(Draught * 10))) AS draught10,
		if(Destination = 'Unknown', '', Destination) AS destination
	FROM ` + from + `
	SETTINGS input_format_skip_unknown_fields = 1`
}

type dmaWord struct {
	word string
	code int
}

// dmaNavStatus is DMA's words for each navigational status; anything else, Unknown value among them, is 15, not
// defined.
var dmaNavStatus = []dmaWord{
	{"Under way using engine", 0}, {"At anchor", 1}, {"Not under command", 2}, {"Restricted maneuverability", 3},
	{"Constrained by her draught", 4}, {"Moored", 5}, {"Aground", 6}, {"Engaged in fishing", 7}, {"Under way sailing", 8},
	{"Reserved for future amendment [HSC]", 9}, {"Reserved for future amendment [WIG]", 10},
	{"Power-driven vessel towing astern", 11}, {"Power-driven vessel pushing ahead or towing alongside", 12},
	{"Reserved for future use", 13},
}

// dmaShipType is DMA's words for each ship type category; anything else is 0, not available.
var dmaShipType = []dmaWord{
	{"WIG", 20}, {"Fishing", 30}, {"Towing", 31}, {"Towing long/wide", 32}, {"Dredging", 33}, {"Diving", 34}, {"Military", 35},
	{"Sailing", 36}, {"Pleasure", 37}, {"Reserved", 38}, {"HSC", 40}, {"Pilot", 50}, {"SAR", 51}, {"Tug", 52}, {"Port tender", 53},
	{"Anti-pollution", 54}, {"Law enforcement", 55}, {"Spare 1", 56}, {"Spare 2", 57}, {"Medical", 58}, {"Not party to conflict", 59},
	{"Passenger", 60}, {"Cargo", 70}, {"Tanker", 80}, {"Other", 90},
}

func dmaWords(ws []dmaWord) string {
	var out []string
	for _, w := range ws {
		out = append(out, sqlString(w.word))
	}
	return strings.Join(out, ", ")
}

func dmaCodes(ws []dmaWord) string {
	var out []string
	for _, w := range ws {
		out = append(out, strconv.Itoa(w.code))
	}
	return strings.Join(out, ", ")
}

// dmaList lists the archive's days, each a zip and the CSV inside it to read. A daily zip holds one CSV, read
// whatever its name. A monthly zip gives a file for each day of its month, named by the zip and the day, which
// reads the CSV named for the day: aisdk_20180115.csv until 2019 and aisdk-2019-01-15.csv after. A day the zip
// lacks reads no rows. The days share the zip's ETag, so a changed zip loads every one of them again.
func dmaList(ctx context.Context) ([]historyFile, error) {
	objs, err := dmaBucket.list(ctx, "")
	if err != nil {
		return nil, err
	}
	var files []historyFile
	for _, o := range objs {
		m := dmaKey.FindStringSubmatch(o.Key)
		if m == nil {
			continue // the README, and anything else that is not a zip of days
		}
		url, etag := dmaBucket.endpoint+"/"+dmaBucket.bucket+"/"+o.Key, strings.Trim(o.ETag, `"`)
		if m[2] != "" {
			day, err := time.Parse("2006-01-02", m[1]+m[2])
			if err != nil {
				continue
			}
			files = append(files, historyFile{name: o.Key, url: url + " :: *.csv", day: day, size: o.Size, etag: etag})
			continue
		}
		month, err := time.Parse("2006-01", m[1])
		if err != nil {
			continue
		}
		for day := month; day.Month() == month.Month(); day = day.AddDate(0, 0, 1) {
			entry := " :: *{" + day.Format("20060102") + "," + day.Format("2006-01-02") + "}.csv"
			files = append(files, historyFile{name: o.Key + " :: " + day.Format("2006-01-02"), url: url + entry, day: day, size: o.Size, etag: etag})
		}
	}
	return files, nil
}
