package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// The normalized archive: every accepted event, every reception copy, and BarentsWatch weather,
// written at emit time as line-delimited JSON envelopes into one merged hourly stream. The raw
// archive stays the input log; this layer is what downstream reads. See specs/normalized-archive.md.

const normVersion = 1

// normEnvelope wraps every record, so the archive's schema is independent of the public stream's:
// a v1 API change is a new envelope version here, never a silent format drift.
type normEnvelope struct {
	K           string          `json:"k"` // event | copy | methyd
	V           int             `json:"v"`
	T           string          `json:"t"` // receive time, RFC3339Nano UTC
	Implausible bool            `json:"implausible,omitempty"`
	Stale       bool            `json:"stale,omitempty"`
	R           json.RawMessage `json:"r"`
}

// normCopy is one delivery of a message: the license belongs here, on the delivery, not on the
// message, and the first copy gets one too since the event record's time is canonical, not receive.
type normCopy struct {
	ID      string `json:"id"`
	Source  string `json:"source"`
	Station string `json:"station"`
	License string `json:"license"`
}

// newNormArchive is the archive writer configured for the merged normalized stream.
func newNormArchive(dir string, s3 *s3Client) *archive {
	a := newArchive(dir, s3)
	a.bare = true
	a.keyFn = func(_ string, hour time.Time) string { return filepath.Join("v1", hour.Format("2006/01/02/15")+".gz") }
	return a
}

// s3NormFromEnv: NORMALIZED_BUCKET with the same account and keys as the raw archive; empty = local only.
func s3NormFromEnv() *s3Client {
	bucket := os.Getenv("NORMALIZED_BUCKET")
	if bucket == "" {
		return nil
	}
	c := &s3Client{bucket: bucket, region: env("S3_REGION", "auto"), accessKey: os.Getenv("R2_ACCESS_KEY_ID"), secretKey: os.Getenv("R2_SECRET_ACCESS_KEY")}
	c.endpoint = env("S3_ENDPOINT", "https://"+os.Getenv("R2_ACCOUNT_ID")+".r2.cloudflarestorage.com")
	if c.accessKey == "" || c.secretKey == "" {
		return nil
	}
	return c
}

func (p *Pipeline) normWrite(kind string, recv time.Time, flags *Event, rec any) {
	if p.norm.dir == "" {
		return
	}
	if !p.normGate.IsZero() && recv.Before(p.normGate) {
		return // replay warm-up: state-building only
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		return
	}
	e := normEnvelope{K: kind, V: normVersion, T: recv.UTC().Format(time.RFC3339Nano), R: raw}
	if flags != nil {
		e.Implausible, e.Stale = flags.Implausible, flags.Stale
	}
	line, err := json.Marshal(e)
	if err != nil {
		return
	}
	p.norm.write(Reception{Source: "norm", RecvTime: recv, Body: string(line)})
}

// writeEvent records an accepted message and its first copy. Runs after updateVessel, so the
// implausible and stale flags are settled; flagged events are archived and not emitted, matching raw.
func (p *Pipeline) writeEvent(ev *Event, key string) {
	if p.norm.dir == "" {
		return
	}
	p.normWrite("event", ev.RecvTime, ev, renderV1(ev))
	p.normWrite("copy", ev.RecvTime, nil, normCopy{ID: ev.ID, Source: ev.Source, Station: ev.Station, License: licenseOf(ev.Source)})
}

// writeCopy records a deduplicated delivery. The id is the same content hash the accepted copy got,
// so it is computable from the duplicate alone.
func (p *Pipeline) writeCopy(ev *Event, key string) {
	if p.norm.dir == "" {
		return
	}
	sum := sha256.Sum256([]byte(key))
	p.normWrite("copy", ev.RecvTime, nil, normCopy{ID: hex.EncodeToString(sum[:16]), Source: ev.Source, Station: ev.Station, License: licenseOf(ev.Source)})
}

// writeMetHyd archives one BarentsWatch weather broadcast verbatim: decoded type-8 sea state, wind,
// and water level that never map onto an AIS packet.
func (p *Pipeline) writeMetHyd(line []byte, recv time.Time) {
	p.normWrite("methyd", recv, nil, json.RawMessage(line))
}

// ---- dedupe window persistence: saved on clean shutdown beside the vessel snapshot, so a deploy
// cannot re-accept a copy inside the window. A crash loses it; the packager collapses those. ----

type dedupeState struct {
	HW   time.Time         `json:"hw"`
	Seen map[string]string `json:"seen"` // base64(payload+channel) -> event time, RFC3339Nano
}

func (p *Pipeline) saveDedupe(path string) error {
	p.mu.Lock()
	st := dedupeState{HW: p.seenHW, Seen: make(map[string]string, len(p.seen))}
	cutoff := p.seenHW.Add(-6 * dedupeWindow)
	for k, v := range p.seen {
		if !v.Before(cutoff) {
			st.Seen[base64.StdEncoding.EncodeToString([]byte(k))] = v.UTC().Format(time.RFC3339Nano)
		}
	}
	p.mu.Unlock()
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (p *Pipeline) loadDedupe(path string) (int, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	var st dedupeState
	if err := json.Unmarshal(b, &st); err != nil {
		return 0, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for k64, ts := range st.Seen {
		k, err := base64.StdEncoding.DecodeString(k64)
		if err != nil {
			continue
		}
		t, err := time.Parse(time.RFC3339Nano, ts)
		if err != nil {
			continue
		}
		p.seen[string(k)] = t
	}
	if st.HW.After(p.seenHW) {
		p.seenHW = st.HW
	}
	return len(p.seen), nil
}
