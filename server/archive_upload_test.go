package main

import (
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// reorderStore models two PUTs of one key in flight at once. The first upload of the target key
// streams the file, then waits on its response while a later upload of the same key overtakes it,
// so the earlier and shorter body lands last. That is how a reopened hour overwrites itself.
type reorderStore struct {
	target   string
	mu       sync.Mutex
	objects  map[string][]byte
	seen     map[string]int
	readDone chan struct{} // closed once the first upload of target has read the file
	overtake chan struct{} // closed once a later upload of target has stored
}

func newReorderStore(target string) *reorderStore {
	return &reorderStore{target: target, objects: map[string][]byte{}, seen: map[string]int{},
		readDone: make(chan struct{}), overtake: make(chan struct{})}
}

func (r *reorderStore) put(key, path string) error {
	b, err := os.ReadFile(path) // a real PUT streams the file, then waits on the response
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.seen[key]++
	first := key == r.target && r.seen[key] == 1
	r.mu.Unlock()

	if first {
		close(r.readDone)
		select {
		case <-r.overtake: // a later upload of this key stored; now finish, last and stale
		case <-time.After(300 * time.Millisecond): // serialized: no later upload can be running
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.objects[key] = b
	if key == r.target && !first {
		select {
		case <-r.overtake:
		default:
			close(r.overtake)
		}
	}
	return nil
}

func (r *reorderStore) size(key string) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return int64(len(r.objects[key])), nil
}

func (r *reorderStore) object(key string) []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.objects[key]
}

// A reception queued across the hour boundary reopens the hour it names, so one key is closed and
// uploaded twice. The bucket must end up with the complete hour however the PUTs interleave.
func TestReopenedHourUploadsCompleteDespiteReordering(t *testing.T) {
	dir := t.TempDir()
	hour := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	a := &archive{dir: dir, ch: make(chan Reception, 8192), done: make(chan chan struct{})}
	key := a.key("kystverket", hour)
	store := newReorderStore(key)
	a.s3 = store
	go a.run()

	a.write(Reception{Source: "kystverket", Station: "kystverket", RecvTime: hour.Add(30 * time.Minute), Body: "early"})
	a.write(Reception{Source: "kystverket", Station: "kystverket", RecvTime: hour.Add(time.Hour), Body: "next-hour"})        // rotates: uploads hour 12
	<-store.readDone                                                                                                         // that upload now holds the short hour
	a.write(Reception{Source: "kystverket", Station: "kystverket", RecvTime: hour.Add(59 * time.Minute), Body: "straggler"}) // reopens hour 12
	a.shutdown()

	body := store.object(key)
	if body == nil {
		t.Fatalf("hour %s never uploaded", key)
	}
	lines := gunzipLines(t, body)
	if len(lines) != 2 || !hasLine(lines, "early") || !hasLine(lines, "straggler") {
		t.Fatalf("bucket holds a truncated hour: %v", lines)
	}
	if _, stillHeld := a.held.Load(a.dirPath(key)); stillHeld {
		t.Fatal("path still held after shutdown") // the refcount must survive two uploads of one file
	}
}

// Two closes of the same key must not both be in flight at once.
func TestUploadsOfOneKeyAreSerialized(t *testing.T) {
	dir := t.TempDir()
	store := &concurrentStore{objects: map[string][]byte{}}
	a := newArchive(dir, store)
	hour := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	for i, rx := range []time.Time{hour, hour.Add(time.Hour), hour.Add(30 * time.Minute), hour.Add(time.Hour)} {
		a.write(Reception{Source: "kystverket", Station: "kystverket", RecvTime: rx, Body: string(rune('a' + i))})
	}
	a.shutdown()
	if n := store.maxConcurrentPerKey(); n > 1 {
		t.Fatalf("%d concurrent uploads of one key", n)
	}
}

type concurrentStore struct {
	mu      sync.Mutex
	objects map[string][]byte
	inFlyN  map[string]int
	maxN    int
}

func (c *concurrentStore) put(key, path string) error {
	c.mu.Lock()
	if c.inFlyN == nil {
		c.inFlyN = map[string]int{}
	}
	c.inFlyN[key]++
	if c.inFlyN[key] > c.maxN {
		c.maxN = c.inFlyN[key]
	}
	c.mu.Unlock()
	time.Sleep(20 * time.Millisecond) // widen the window a racing upload would land in
	b, err := os.ReadFile(path)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.inFlyN[key]--
	if err == nil {
		c.objects[key] = b
	}
	return err
}

func (c *concurrentStore) size(key string) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return int64(len(c.objects[key])), nil
}

func (c *concurrentStore) maxConcurrentPerKey() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.maxN
}

func gunzipLines(t *testing.T, b []byte) []string {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("bucket object is not valid gzip: %v", err)
	}
	raw, err := io.ReadAll(gz)
	if err != nil {
		t.Fatalf("bucket object truncated: %v", err)
	}
	var out []string
	for _, l := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

func hasLine(lines []string, want string) bool {
	for _, l := range lines {
		if strings.Contains(l, want) {
			return true
		}
	}
	return false
}
