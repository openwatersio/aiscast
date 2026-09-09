package main

import (
	"compress/gzip"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// license tag per source; goes in the object path so consumers can filter by terms.
// All volunteer receptions are CC0 per the contributor agreement and docs/policy.md.
var licenses = map[string]string{
	"kystverket": "NLOD-2.0", "barentswatch": "NLOD-2.0", "digitraffic": "CC-BY-4.0", "aisstream": "aisstream-io-terms", "aishub": "aishub-terms",
	"v1": "CC0-1.0", "http": "CC0-1.0", "udp": "CC0-1.0", "mmsi": "CC0-1.0",
}

// licenseOf resolves a source's license tag: the full source name first, then its prefix (`v1:ed25519:...` → `v1`).
func licenseOf(source string) string {
	if lic := licenses[source]; lic != "" {
		return lic
	}
	if i := strings.IndexByte(source, ':'); i > 0 {
		if lic := licenses[source[:i]]; lic != "" {
			return lic
		}
	}
	return "unspecified"
}

type hourFile struct {
	hour time.Time
	path string
	f    *os.File
	gz   *gzip.Writer
}

// archive writes every reception source-native to hourly gzip files per source, then uploads to R2 when rotated.
type archive struct {
	dir     string
	s3      *s3Client // nil = keep files local only
	ch      chan Reception
	done    chan chan struct{} // shutdown request; replied to when files are closed and uploaded
	drops   atomic.Int64
	uploads sync.WaitGroup

	// held is every path run() still owns, including one being uploaded. The sweep skips these: a
	// quiet source keeps its hour open indefinitely, and deleting it out from under the writer would
	// strand the gzip footer. Zero value is usable, so tests can build an archive as a literal.
	held sync.Map
}

// newArchive with an empty dir is a no-op archive (tests).
func newArchive(dir string, s3 *s3Client) *archive {
	a := &archive{dir: dir, s3: s3, ch: make(chan Reception, 8192), done: make(chan chan struct{})}
	if dir != "" {
		go a.run()
	}
	return a
}

func (a *archive) write(rx Reception) {
	if a.dir == "" {
		return
	}
	select {
	case a.ch <- rx:
	default: // drop rather than stall ingest
		a.drops.Add(1)
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
		case reply := <-a.done:
			for _, hf := range files {
				a.close(hf)
			}
			a.uploads.Wait()
			reply <- struct{}{}
			return
		}
	}
}

// shutdown closes open hours (uploading them) and waits up to 90 s.
func (a *archive) shutdown() {
	if a.dir == "" {
		return
	}
	reply := make(chan struct{})
	select {
	case a.done <- reply:
		select {
		case <-reply:
		case <-time.After(90 * time.Second):
			log.Printf("archive: shutdown timed out waiting for uploads")
		}
	case <-time.After(5 * time.Second):
	}
}

func (a *archive) key(source string, hour time.Time) string {
	return filepath.Join(licenseOf(source), strings.ReplaceAll(source, ":", "/"), hour.Format("2006/01/02/15")+".gz")
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
	a.held.Store(path, struct{}{})
	return &hourFile{hour: hour, path: path, f: f, gz: gzip.NewWriter(f)} // appending gzip members is valid gzip
}

func (a *archive) close(hf *hourFile) {
	gzErr, fErr := hf.gz.Close(), hf.f.Close()
	if gzErr != nil || fErr != nil {
		// The hour on disk may be short. Upload it anyway, since it is the only copy, but say so:
		// silence here looks identical to a healthy rotation.
		log.Printf("archive: close %s: gzip=%v file=%v", hf.path, gzErr, fErr)
	}
	if a.s3 == nil {
		a.held.Delete(hf.path)
		return
	}
	rel, _ := filepath.Rel(a.dir, hf.path)
	a.uploads.Add(1)
	go func() {
		defer a.uploads.Done()
		defer a.held.Delete(hf.path) // stay held until the upload is done, so no sweep deletes it mid-put
		if err := a.s3.put(filepath.ToSlash(rel), hf.path); err != nil {
			log.Printf("archive: upload %s: %v", rel, err) // the next sweep retries it
			return
		}
		log.Printf("archive: uploaded %s", rel)
	}()
}

// archiveGrace is how long an hour file must sit untouched before a sweep may delete it. Rotation
// does not delete: a Reception queued across the hour boundary reopens the hour it names, appending
// to the file and uploading it again, so a file deleted at rotation would come back as a stub and
// overwrite the complete object in the bucket. Receive time is our own clock, so nothing reopens an
// hour this old. The grace period covers files a previous process left behind, which are on disk but
// not in held; files this process still owns are excluded by held, not by their age.
const archiveGrace = 2 * time.Hour

// sweep reconciles the local tree with the bucket, which is where the archive actually lives; disk is
// only staging. A file the bucket already holds at the same size is deleted, and a short or missing
// one is uploaded first. An object larger than the local file is left alone: that is a stub over a
// complete upload, and overwriting it would destroy the only good copy.
func (a *archive) sweep() {
	if a.dir == "" || a.s3 == nil {
		return
	}
	cutoff := time.Now().Add(-archiveGrace)
	var freed, kept int64
	filepath.WalkDir(a.dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".gz") {
			return nil
		}
		if _, open := a.held.Load(path); open {
			return nil
		}
		fi, err := d.Info()
		if err != nil || fi.ModTime().After(cutoff) {
			return nil
		}
		rel, err := filepath.Rel(a.dir, path)
		if err != nil {
			return nil
		}
		key := filepath.ToSlash(rel)
		stored, err := a.s3.size(key)
		if err != nil {
			log.Printf("archive: sweep head %s: %v", key, err)
			kept += fi.Size()
			return nil
		}
		switch {
		case stored > fi.Size():
			log.Printf("archive: %s is %d bytes in the bucket but %d on disk; keeping both for a human", key, stored, fi.Size())
			kept += fi.Size()
			return nil
		case stored < fi.Size():
			if err := a.s3.put(key, path); err != nil {
				log.Printf("archive: sweep upload %s: %v", key, err)
				kept += fi.Size()
				return nil
			}
			log.Printf("archive: uploaded %s (sweep)", key)
		}
		// The upload took a while. Delete only the bytes that went up: anything else means a writer
		// touched the file, and the next sweep can take another run at it.
		if cur, err := os.Stat(path); err != nil || cur.Size() != fi.Size() || !cur.ModTime().Equal(fi.ModTime()) {
			kept += fi.Size()
			return nil
		}
		if err := os.Remove(path); err != nil {
			log.Printf("archive: sweep remove %s: %v", key, err)
			kept += fi.Size()
			return nil
		}
		freed += fi.Size()
		return nil
	})
	log.Printf("archive: sweep freed %d MiB, kept %d MiB not yet reclaimed", freed>>20, kept>>20)
}

// sweepLoop reclaims on an interval, so a failed upload is retried without waiting for a restart.
func (a *archive) sweepLoop() {
	for {
		a.sweep()
		time.Sleep(time.Hour)
	}
}
