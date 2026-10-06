package main

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"time"
)

// The dedupe window is saved on a clean shutdown and restored at start, so a deploy cannot accept again a copy
// inside the window. A crash loses it.

type dedupeState struct {
	HW   time.Time         `json:"hw"`
	Seen map[string]string `json:"seen"` // base64(payload+channel) -> event time, RFC3339Nano
}

// saveDedupe keeps entries by the same event-time window the live prune uses, so a canonical time
// minutes behind the high water (an AISHub row) ages out of restart protection exactly as it ages
// out live.
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
