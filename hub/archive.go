package main

import (
	"compress/gzip"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// license tag per source; goes in the object path so consumers can filter by terms.
var licenses = map[string]string{"kystverket": "NLOD-2.0"}

type hourFile struct {
	hour time.Time
	path string
	f    *os.File
	gz   *gzip.Writer
}

// archive writes every reception source-native to hourly gzip files per source, then uploads to R2 when rotated.
type archive struct {
	dir, bucket string
	ch          chan Reception
}

func newArchive(dir, bucket string) *archive {
	a := &archive{dir: dir, bucket: bucket, ch: make(chan Reception, 8192)}
	go a.run()
	return a
}

func (a *archive) write(rx Reception) {
	select {
	case a.ch <- rx:
	default: // ponytail: drop rather than stall ingest; count this when /metrics exists
	}
}

func (a *archive) run() {
	files := map[string]*hourFile{}
	flush := time.NewTicker(5 * time.Second)
	for {
		select {
		case rx := <-a.ch:
			hour := rx.RecvTime.UTC().Truncate(time.Hour)
			hf := files[rx.Source]
			if hf != nil && !hf.hour.Equal(hour) {
				a.close(hf)
				hf = nil
			}
			if hf == nil {
				hf = a.open(rx.Source, hour)
				if hf == nil {
					continue
				}
				files[rx.Source] = hf
			}
			// one record per line: recv time, station, body as received (JSON envelopes are single-line)
			hf.gz.Write([]byte(rx.RecvTime.UTC().Format(time.RFC3339Nano) + "\t" + rx.Station + "\t" + strings.TrimRight(rx.Body, "\r\n") + "\n"))
		case <-flush.C:
			for _, hf := range files {
				hf.gz.Flush()
			}
		}
	}
}

func (a *archive) key(source string, hour time.Time) string {
	lic := licenses[source]
	if lic == "" {
		lic = "feeder"
	}
	return filepath.Join(lic, strings.ReplaceAll(source, ":", "/"), hour.Format("2006/01/02/15")+".gz")
}

func (a *archive) open(source string, hour time.Time) *hourFile {
	path := filepath.Join(a.dir, a.key(source, hour))
	if rel, err := filepath.Rel(a.dir, path); err != nil || strings.HasPrefix(rel, "..") {
		log.Printf("archive: refusing path outside archive dir for source %q", source)
		return nil
	}
	os.MkdirAll(filepath.Dir(path), 0o755)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		log.Printf("archive: %v", err)
		return nil
	}
	return &hourFile{hour: hour, path: path, f: f, gz: gzip.NewWriter(f)} // appending gzip members is valid gzip
}

func (a *archive) close(hf *hourFile) {
	hf.gz.Close()
	hf.f.Close()
	if a.bucket == "" {
		return
	}
	rel, _ := filepath.Rel(a.dir, hf.path)
	go func() {
		// ponytail: shell out to wrangler (already logged in); swap for an S3 SigV4 client when this runs unattended
		out, err := exec.Command("npx", "-y", "wrangler", "r2", "object", "put", a.bucket+"/"+rel, "--file", hf.path, "--remote").CombinedOutput()
		if err != nil {
			log.Printf("archive: upload %s: %v: %s", rel, err, out)
			return
		}
		log.Printf("archive: uploaded %s", rel)
	}()
}
