package main

// MarineCadastre: the U.S. Coast Guard's Nationwide AIS, published by NOAA and BOEM as one zstd CSV a day on
// Azure blob storage, about two months behind, a quarter at a time. Public domain. The csv2 release has one
// schema for 2015 on; each vessel has at most one position a minute, its stamps keep their seconds, and
// coordinates have five decimal places, so a row may sit up to recentNearA wire units from the copy it repeats.
// Alaska ends on 2021-03-28, at the request of the Marine Exchange of Alaska.

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

// mcContainer is the Azure container; listing it is anonymous and pages by marker.
var mcContainer = "https://noaaocm.blob.core.windows.net/ais"

// mcColumns is csv2's header, typed. Every column but the first four and the transceiver class may be empty.
const mcColumns = `mmsi String, base_date_time String, longitude Nullable(Float64), latitude Nullable(Float64), sog Nullable(Float64),
	cog Nullable(Float64), heading Nullable(Float64), vessel_name Nullable(String), imo Nullable(String), call_sign Nullable(String),
	vessel_type Nullable(Int32), status Nullable(Int32), length Nullable(Float64), width Nullable(Float64), draft Nullable(Float64),
	cargo Nullable(String), transceiver Nullable(String)`

// marineCadastre is the source, loading from from on. MARINECADASTRE turns it on.
func marineCadastre(from time.Time) historySource {
	return historySource{name: "marinecadastre", from: from, read: mcRead,
		list: func(ctx context.Context) ([]historyFile, error) { return mcList(ctx, from) }}
}

// mcRead reads one day in the server's encodings. Empty values become the not-available ones, and an IMO
// number loses its "IMO" prefix.
func mcRead(f historyFile) string {
	return mcSelect("url(" + sqlString(f.url) + ", 'CSVWithNames', " + sqlString(mcColumns) + ")")
}

// mcSelect maps csv2 rows from a table function. Tests read from format() instead of a URL.
func mcSelect(from string) string {
	return `SELECT toUInt32OrZero(mmsi) AS mmsi, parseDateTime64BestEffortOrZero(base_date_time, 3, 'UTC') AS ts,
		toInt32(round(coalesce(latitude, 91) * 600000)) AS lat6, toInt32(round(coalesce(longitude, 181) * 600000)) AS lon6,
		if(sog IS NULL OR sog < 0 OR sog >= 102.3, 1023, toUInt16(round(sog * 10))) AS sog10,
		if(cog IS NULL OR cog < 0 OR cog >= 360, 3600, toUInt16(round(cog * 10))) AS cog10,
		if(heading IS NULL OR heading < 0 OR heading >= 360, 511, toUInt16(heading)) AS heading,
		if(status IS NULL OR status < 0 OR status > 14, 15, toUInt8(status)) AS navstat,
		coalesce(vessel_name, '') AS name, coalesce(call_sign, '') AS callsign,
		toUInt32OrZero(replaceRegexpOne(coalesce(imo, ''), '^IMO', '')) AS imo,
		if(vessel_type IS NULL OR vessel_type < 0 OR vessel_type > 255, 0, toUInt8(vessel_type)) AS ship_type,
		if(length IS NULL OR length <= 0 OR length > 1000, 0, toUInt16(round(length))) AS length,
		if(width IS NULL OR width <= 0 OR width > 1000, 0, toUInt16(round(width))) AS beam,
		if(draft IS NULL OR draft <= 0 OR draft > 50, 0, toUInt16(round(draft * 10))) AS draught10,
		'' AS destination
	FROM ` + from
}

// mcList lists csv2's daily files for every year from from's to now.
func mcList(ctx context.Context, from time.Time) ([]historyFile, error) {
	var files []historyFile
	for year := from.Year(); year <= time.Now().UTC().Year(); year++ {
		got, err := azureList(ctx, mcContainer, fmt.Sprintf("csv2/csv%d/", year))
		if err != nil {
			return nil, err
		}
		for _, b := range got {
			day, err := time.Parse("2006-01-02", strings.TrimSuffix(strings.TrimPrefix(path.Base(b.Name), "ais-"), ".csv.zst"))
			if err != nil || !strings.HasSuffix(b.Name, ".csv.zst") {
				continue // the container's index.html, and anything else that is not a day
			}
			files = append(files, historyFile{name: b.Name, url: mcContainer + "/" + b.Name, day: day, size: b.Size, etag: b.ETag})
		}
	}
	return files, nil
}

type azureBlob struct {
	Name string `xml:"Name"`
	Size int64  `xml:"Properties>Content-Length"`
	ETag string `xml:"Properties>Etag"`
}

// azureList lists a public container's blobs under prefix, following NextMarker.
func azureList(ctx context.Context, container, prefix string) ([]azureBlob, error) {
	var out []azureBlob
	marker := ""
	for {
		q := url.Values{"restype": {"container"}, "comp": {"list"}, "prefix": {prefix}}
		if marker != "" {
			q.Set("marker", marker)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, container+"?"+q.Encode(), nil)
		if err != nil {
			return nil, err
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, err
		}
		var page struct {
			Blobs      []azureBlob `xml:"Blobs>Blob"`
			NextMarker string      `xml:"NextMarker"`
		}
		err = xml.NewDecoder(resp.Body).Decode(&page)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("list %s: %s", prefix, resp.Status)
		}
		if err != nil {
			return nil, fmt.Errorf("list %s: %w", prefix, err)
		}
		out = append(out, page.Blobs...)
		if page.NextMarker == "" {
			return out, nil
		}
		marker = page.NextMarker
	}
}
