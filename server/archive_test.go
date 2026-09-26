package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// The bucket is the archive and disk is staging, so a sweep must delete only what the bucket already
// holds intact. A truncated object is the real failure mode: a PUT that dies mid-body leaves a short
// object behind, and deleting the local file on a bare existence check would lose the rest.
func TestArchiveSweep(t *testing.T) {
	dir := t.TempDir()
	stored := map[string][]byte{
		"a/stored.gz":    []byte("hello"),
		"a/truncated.gz": []byte("he"),
		"a/stub.gz":      []byte("hello there, the complete hour"),
	}
	var puts []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Path[len("/b/"):]
		switch r.Method {
		case http.MethodHead:
			b, ok := stored[key]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Length", strconv.Itoa(len(b)))
		case http.MethodPut:
			if key == "a/broken.gz" {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			puts = append(puts, key)
		}
	}))
	defer srv.Close()

	write := func(name, body string, age time.Duration) string {
		path := filepath.Join(dir, filepath.FromSlash(name))
		os.MkdirAll(filepath.Dir(path), 0o755)
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		mt := time.Now().Add(-age)
		os.Chtimes(path, mt, mt)
		return path
	}

	cases := []struct {
		name, body string
		age        time.Duration
		wantGone   bool
	}{
		{"a/stored.gz", "hello", 2 * time.Hour, true},    // already in the bucket at full size
		{"a/truncated.gz", "hello", 2 * time.Hour, true}, // short object upstream, re-upload then drop
		{"a/missing.gz", "hello", 2 * time.Hour, true},   // never made it up, upload then drop
		{"a/broken.gz", "hello", 2 * time.Hour, false},   // upload fails, keep it for the next sweep
		{"a/stub.gz", "hello", 2 * time.Hour, false},     // local is a stub over a complete object: never clobber it
		{"a/open.gz", "hello", 5 * time.Minute, false},   // inside the grace window, rotation owns it
	}
	paths := map[string]string{}
	for _, tc := range cases {
		paths[tc.name] = write(tc.name, tc.body, tc.age)
	}

	a := &archive{dir: dir, s3: &s3Client{endpoint: srv.URL, bucket: "b", region: "auto", accessKey: "k", secretKey: "s"}}
	a.sweep()

	for _, tc := range cases {
		_, err := os.Stat(paths[tc.name])
		if gone := os.IsNotExist(err); gone != tc.wantGone {
			t.Errorf("%s: deleted=%v, want %v", tc.name, gone, tc.wantGone)
		}
	}
	want := map[string]bool{"a/truncated.gz": true, "a/missing.gz": true}
	for _, k := range puts {
		if !want[k] {
			t.Errorf("unexpected upload of %s", k)
		}
		delete(want, k)
	}
	for k := range want {
		t.Errorf("%s was never uploaded", k)
	}
	if got := a.staged.Load(); got != 15 { // broken, stub, and open stay on disk
		t.Errorf("staged = %d bytes, want 15", got)
	}
	if got := a.uploadFailures.Load(); got != 1 {
		t.Errorf("upload failures = %d, want 1", got)
	}
}

// Rotation uploads but must not delete: a Reception queued across the hour boundary reopens the same
// path, and a file deleted here would come back as a stub and overwrite the complete object.
func TestArchiveCloseKeepsFileForTheSweep(t *testing.T) {
	dir := t.TempDir()
	var puts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			puts++
		}
	}))
	defer srv.Close()

	a := &archive{dir: dir, s3: &s3Client{endpoint: srv.URL, bucket: "b", region: "auto", accessKey: "k", secretKey: "s"}}
	hf := a.open("kystverket", time.Date(2026, 9, 1, 3, 0, 0, 0, time.UTC))
	if hf == nil {
		t.Fatal("open returned nil")
	}
	hf.gz.Write([]byte("line\n"))
	a.close(hf)
	a.uploads.Wait()

	if puts != 1 {
		t.Errorf("uploads = %d, want 1", puts)
	}
	if _, err := os.Stat(hf.path); err != nil {
		t.Errorf("rotation deleted %s; only the sweep may delete: %v", hf.path, err)
	}
}

// A quiet source keeps its hour file open indefinitely, so an open file can be older than the grace
// period. The sweep must leave it alone: deleting it would strand the gzip footer the writer still
// owes, and the mtime check alone only happens to cover this because an idle gzip flush writes bytes.
func TestArchiveSweepSkipsOpenFiles(t *testing.T) {
	dir := t.TempDir()
	var touched []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		touched = append(touched, r.Method+" "+r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	a := &archive{dir: dir, s3: &s3Client{endpoint: srv.URL, bucket: "b", region: "auto", accessKey: "k", secretKey: "s"}}
	hf := a.open("kystverket", time.Date(2026, 9, 1, 3, 0, 0, 0, time.UTC))
	if hf == nil {
		t.Fatal("open returned nil")
	}
	hf.gz.Write([]byte("line\n"))
	hf.gz.Flush()

	// Age it well past the grace window, as a source that has been silent for hours would be.
	old := time.Now().Add(-6 * time.Hour)
	os.Chtimes(hf.path, old, old)

	a.sweep()

	if _, err := os.Stat(hf.path); err != nil {
		t.Errorf("sweep deleted an open file: %v", err)
	}
	if len(touched) != 0 {
		t.Errorf("sweep touched the bucket for an open file: %v", touched)
	}

	// Once rotation hands it over, the sweep owns it again.
	a.close(hf)
	a.uploads.Wait()
	os.Chtimes(hf.path, old, old)
	a.sweep()
	if _, err := os.Stat(hf.path); err != nil {
		t.Errorf("sweep should reclaim a closed file: %v", err)
	}
}

func TestLicenseAndAttribution(t *testing.T) {
	cases := []struct{ source, license, attribution string }{
		{"kystverket", "NLOD-2.0", ownCredit + ". " + attributions["kystverket"]},
		{"station:ed25519:abc", "CC0-1.0", ownCredit},
		{"udp:7f3a2b", "CC0-1.0", ownCredit},
		{"mystery", "unspecified", ownCredit},
	}
	for _, c := range cases {
		if got := licenseOf(c.source); got != c.license {
			t.Errorf("licenseOf(%q) = %q, want %q", c.source, got, c.license)
		}
		if got := attributionOf(c.source); got != c.attribution {
			t.Errorf("attributionOf(%q) = %q, want %q", c.source, got, c.attribution)
		}
	}
	for src, lic := range licenses { // an upstream source without its credit line would ship under-attributed events
		if lic != "CC0-1.0" && attributions[src] == "" {
			t.Errorf("upstream source %q has a license but no attribution", src)
		}
	}
	ev := renderV1(&Event{Source: "digitraffic"})
	if ev.License != "CC-BY-4.0" || ev.Attribution != ownCredit+". "+attributions["digitraffic"] {
		t.Errorf("renderV1 license/attribution = %q/%q", ev.License, ev.Attribution)
	}
}
