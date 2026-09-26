package main

import (
	"compress/gzip"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// license tag per source; goes in the object path so consumers can filter by terms, and on every /v1
// event. All volunteer receptions are CC0 per the contributor agreement and docs/policy.md.
var licenses = map[string]string{
	"kystverket": "NLOD-2.0", "barentswatch": "NLOD-2.0", "digitraffic": "CC-BY-4.0", "aisstream": "aisstream-io-terms", "aishub": "aishub-terms",
	"station": "CC0-1.0", "udp": "CC0-1.0", "mmsi": "CC0-1.0",
	// contributors were named by transport before station ids; raw hours from then still carry these
	// sources, and replaying them must license their copies the same way
	"http": "CC0-1.0", "v1": "CC0-1.0",
}

// ownCredit opens every event's attribution: the only credit for volunteer stations, and the prefix to
// the credit an upstream source's terms require.
const ownCredit = "Open Waters AIS (https://openwaters.io/ais/)"

// attributions: the further credit a source's terms require, appended to ownCredit on every /v1 event.
// The strings are the ones the README's licensing table requires; the two tables must not drift.
var attributions = map[string]string{
	"kystverket":   "Contains data under the Norwegian licence for Open Government data (NLOD) distributed by the Norwegian Coastal Administration.",
	"barentswatch": "Data delivered by BarentsWatch. Contains data under the Norwegian licence for Open Government data (NLOD) distributed by the Norwegian Coastal Administration.",
	"digitraffic":  "Source: Fintraffic / digitraffic.fi, license CC 4.0 BY",
	"aishub":       "AISHub (https://www.aishub.net)",
	"aisstream":    "aisstream.io",
}

// licenseOf resolves a source's license tag: the full source name first, then its prefix (`station:ed25519:...` → `station`).
func licenseOf(source string) string { return bySource(licenses, source, "unspecified") }

// attributionOf builds a source's credit line: ownCredit, plus the source's own required credit.
func attributionOf(source string) string {
	if extra := bySource(attributions, source, ""); extra != "" {
		return ownCredit + ". " + extra
	}
	return ownCredit
}

func bySource(m map[string]string, source, def string) string {
	if v := m[source]; v != "" {
		return v
	}
	if i := strings.IndexByte(source, ':'); i > 0 {
		if v := m[source[:i]]; v != "" {
			return v
		}
	}
	return def
}

// objectStore is the bucket as the archive uses it. *s3Client is the real one; tests supply a
// store that can stall and reorder uploads.
type objectStore interface {
	put(key, path string) error
	size(key string) (int64, error)
}

type hourFile struct {
	hour time.Time
	path string
	f    *os.File
	gz   *gzip.Writer
}

// archive writes every reception source-native to hourly gzip files per source, then uploads to R2 when rotated.
// The normalized stream reuses it with keyFn and bare set: one merged file per hour, envelope-only lines.
type archive struct {
	dir     string
	s3      objectStore                                // nil = keep files local only
	keyFn   func(source string, hour time.Time) string // nil = per-source license-prefixed layout
	bare    bool                                       // write Body verbatim, one record per line, instead of the recv/station/body raw format
	ch      chan Reception
	done    chan chan struct{} // shutdown request; replied to when files are closed and uploaded
	uploads sync.WaitGroup

	// held is every path run() still owns, including one being uploaded. The sweep skips these: a
	// quiet source keeps its hour open indefinitely, and deleting it out from under the writer would
	// strand the gzip footer. Zero value is usable, so tests can build an archive as a literal.
	held sync.Map
	// holds counts the open writer and every in-flight upload per path: rotation can close one hour
	// twice, and the first upload to finish must not unprotect a file the second is still reading.
	holds sync.Map // path -> *atomic.Int64
	// putLocks serializes uploads per object key. A reception queued across the hour boundary
	// reopens that hour, so the same key is closed and uploaded more than once; unordered PUTs let
	// the earlier, shorter file land last and leave the bucket holding a truncated hour. Bounded by
	// the distinct hour keys one process touches: hours times sources.
	putLocks sync.Map // key -> *sync.Mutex
}

// newArchive with an empty dir is a no-op archive (tests).
func newArchive(dir string, s3 objectStore) *archive {
	a := &archive{dir: dir, ch: make(chan Reception, 8192), done: make(chan chan struct{})}
	if s3 != nil && !reflect.ValueOf(s3).IsNil() { // a typed-nil *s3Client must stay a nil store
		a.s3 = s3
	}
	if dir != "" {
		go a.run()
	}
	return a
}

func (a *archive) write(rx Reception) {
	if a.dir == "" {
		return
	}
	// Block rather than drop: raw and normalized must hold the same receptions for replay to
	// regenerate the stream, and the writer only touches local disk (uploads run beside it), so a
	// full queue means the disk has stalled and ingest waits for it.
	a.ch <- rx
}

func (a *archive) run() {
	files := map[string]*hourFile{}
	flush := time.NewTicker(5 * time.Second)
	for {
		select {
		case rx := <-a.ch:
			a.handle(rx, files)
		case <-flush.C:
			for _, hf := range files {
				hf.gz.Flush()
			}
		case reply := <-a.done:
			// drain: the select races queued records against shutdown, and the tail must not lose
			for drained := false; !drained; {
				select {
				case rx := <-a.ch:
					a.handle(rx, files)
				default:
					drained = true
				}
			}
			for _, hf := range files {
				a.close(hf)
			}
			a.uploads.Wait()
			reply <- struct{}{}
			return
		}
	}
}

// bufferedMark follows the station of a reception its sender marked as an offline backlog. Station
// ids never contain a space, so the mark cannot collide with one.
const bufferedMark = " buffered"

func (a *archive) handle(rx Reception, files map[string]*hourFile) {
	hour := rx.RecvTime.UTC().Truncate(time.Hour)
	stream := a.key(rx.Source, time.Time{}) // the key with the hour zeroed: one per source raw, one in total merged
	hf := files[stream]
	if hf != nil && !hf.hour.Equal(hour) {
		a.close(hf)
		hf = nil
	}
	if hf == nil {
		hf = a.open(rx.Source, hour)
		if hf == nil {
			return
		}
		files[stream] = hf
	}
	// one record per line: recv time, station, body as received (JSON envelopes are single-line)
	if a.bare {
		hf.gz.Write([]byte(strings.TrimRight(rx.Body, "\r\n") + "\n"))
	} else {
		station := rx.Station
		if rx.Buffered {
			station += bufferedMark // replay needs it to suppress the same stale backlog live did
		}
		hf.gz.Write([]byte(rx.RecvTime.UTC().Format(time.RFC3339Nano) + "\t" + station + "\t" + strings.TrimRight(rx.Body, "\r\n") + "\n"))
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
	if a.keyFn != nil {
		return a.keyFn(source, hour)
	}
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
	a.hold(path)
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
		a.release(hf.path)
		return
	}
	rel, _ := filepath.Rel(a.dir, hf.path)
	key := filepath.ToSlash(rel)
	a.uploads.Add(1)
	go func() {
		defer a.uploads.Done()
		defer a.release(hf.path) // held until every upload of it is done, so no sweep deletes it mid-put
		// One key at a time: put reads the file when its turn comes, so the last upload to run
		// carries the newest bytes and a reopened hour cannot be overwritten by its earlier self.
		a.withKey(key, func() {
			if err := a.s3.put(key, hf.path); err != nil {
				log.Printf("archive: upload %s: %v", rel, err) // the next sweep retries it
				return
			}
			log.Printf("archive: uploaded %s", rel)
		})
	}()
}

// dirPath is the local file an object key came from.
func (a *archive) dirPath(key string) string { return filepath.Join(a.dir, filepath.FromSlash(key)) }

// withKey runs fn holding the upload lock for one object key.
func (a *archive) withKey(key string, fn func()) {
	mu, _ := a.putLocks.LoadOrStore(key, &sync.Mutex{})
	mu.(*sync.Mutex).Lock()
	defer mu.(*sync.Mutex).Unlock()
	fn()
}

// hold marks a path as owned by the writer or an upload; release drops it when the last owner is done.
func (a *archive) hold(path string) {
	c, _ := a.holds.LoadOrStore(path, new(atomic.Int64))
	c.(*atomic.Int64).Add(1)
	a.held.Store(path, struct{}{})
}

func (a *archive) release(path string) {
	if c, ok := a.holds.Load(path); ok && c.(*atomic.Int64).Add(-1) > 0 {
		return
	}
	a.held.Delete(path)
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
			var err error
			a.withKey(key, func() { err = a.s3.put(key, path) }) // never race a rotation upload of the same key
			if err != nil {
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
