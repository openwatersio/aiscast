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
		{"a/open.gz", "hello", 5 * time.Minute, false},   // current hour, rotation owns it
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
}

// A successful rotation upload leaves nothing behind on disk.
func TestArchiveCloseDeletesAfterUpload(t *testing.T) {
	dir := t.TempDir()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()

	a := &archive{dir: dir, s3: &s3Client{endpoint: srv.URL, bucket: "b", region: "auto", accessKey: "k", secretKey: "s"}}
	hf := a.open("kystverket", time.Date(2026, 9, 1, 3, 0, 0, 0, time.UTC))
	if hf == nil {
		t.Fatal("open returned nil")
	}
	hf.gz.Write([]byte("line\n"))
	a.close(hf)
	a.uploads.Wait()

	if _, err := os.Stat(hf.path); !os.IsNotExist(err) {
		t.Errorf("%s still on disk after a successful upload", hf.path)
	}
}
