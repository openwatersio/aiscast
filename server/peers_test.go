package main

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	ais "github.com/BertoldVdb/go-ais"
	"github.com/coder/websocket"
)

func TestPeerable(t *testing.T) {
	cases := []struct {
		source              string
		lowTrust, corr, syn bool
		want                bool
	}{
		{"station:ed25519:k", false, false, false, true},
		{"udp:abc", false, false, false, true},
		{"udp:abc", true, false, false, false},
		{"udp:abc", true, true, false, true},
		{"station:ed25519:k", false, false, true, false},
		{"kystverket", false, false, false, false},
		{"aishub", false, false, false, false},
		{"peer:openwaters", false, false, false, false},
		{"peer:ed25519:k", false, false, false, false},
	}
	for _, c := range cases {
		ev := &Event{Source: c.source, LowTrust: c.lowTrust, Corroborated: c.corr, Synthesized: c.syn, Packet: ais.PositionReport{}}
		if got := peerable(ev); got != c.want {
			t.Errorf("peerable(%+v) = %v, want %v", c, got, c.want)
		}
	}
	if peerable(&Event{Source: "station:ed25519:k", Own: true, Packet: ais.PositionReport{}}) {
		t.Error("an own-ship report is peerable")
	}
	if got := seriesSource("peer:ed25519:k/station:ed25519:x", peerSource); got != "peer:ed25519:k" {
		t.Errorf("a restored peer station's source is %s", got)
	}
	if !lowTrust("peer:ed25519:k") {
		t.Error("a peer's reception is trusted")
	}
}

// peerServer is node B: tokens required, serving /v1/stream under its own node key.
func peerServer(t *testing.T) (*Pipeline, ed25519.PrivateKey, string) {
	t.Helper()
	b := testPipeline(t)
	allowAnon = false
	t.Cleanup(func() { allowAnon = true })
	key := nodeKey(t)
	b.nodeKey, b.nodeSub = key, nodeSub(key)
	srv := httptest.NewServer(httpHandler(b))
	t.Cleanup(srv.Close)
	return b, key, "ws" + strings.TrimPrefix(srv.URL, "http") + "/v1/stream"
}

func nodeKey(t *testing.T) ed25519.PrivateKey {
	_, k, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// next waits for the first event on s that passes keep.
func next(t *testing.T, s *subscriber, keep func(*Event) bool) *Event {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev := <-s.ch:
			if keep(ev) {
				return ev
			}
		case <-deadline:
			t.Fatal("no event")
		}
	}
}

func hostOf(wsURL string) string {
	h, _, _ := strings.Cut(strings.TrimPrefix(wsURL, "ws://"), "/")
	return h
}

func isPeer(ev *Event) bool { return strings.HasPrefix(ev.Source, "peer:") }

// dialPeer runs a.peerOnce against url until the test ends; the cancel runs before srv.Close, which waits for the socket.
func dialPeer(t *testing.T, a *Pipeline, url string, key ed25519.PrivateKey) {
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	go a.peerOnce(ctx, peer{"b", url}, key)
}

func TestPeerSelf(t *testing.T) {
	a := testPipeline(t)
	_, key, url := peerServer(t) // after a: testPipeline turns ALLOW_ANON back on
	if err := a.peerOnce(t.Context(), peer{"b", url}, key); !errors.Is(err, errSelf) {
		t.Fatalf("dialing itself: %v", err)
	}
}

func TestPeerExchange(t *testing.T) {
	a := testPipeline(t)
	b, keyB, url := peerServer(t)
	keyA := nodeKey(t)
	b.approvedPeers = map[string]bool{nodeSub(keyA): true}
	bSub, aSub := b.subscribe(), a.subscribe()
	defer b.unsubscribe(bSub)
	defer a.unsubscribe(aSub)

	dialPeer(t, a, url, keyA)
	deadline := time.Now().Add(5 * time.Second)
	for b.streams.snapshot()[[2]string{"v1", "peer"}] == 0 {
		if time.Now().After(deadline) {
			t.Fatal("A never connected to B as a peer")
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond) // origin=local subscribe in flight

	// A shares its volunteer reception, not its Kystverket one.
	now := time.Now()
	a.Ingest(Reception{Source: "kystverket", Station: "kystverket", RecvTime: now, Body: "!AIVDM,1,1,,A,15NJ5cPP00o?8pHG8CpSWwvP2<1h,0*6E"})
	a.Ingest(Reception{Source: "station:ed25519:x", Station: "station:ed25519:x", RecvTime: now, Body: "!AIVDM,1,1,,A,13HOI:0P0000VOHLCnHQKwvL05Ip,0*23"})
	ev := next(t, bSub, isPeer)
	if ev.Source != "peer:"+nodeSub(keyA) || ev.Station != ev.Source+"/station:ed25519:x" || ev.MMSI != 227006760 {
		t.Errorf("B got %s from %s", ev.Station, ev.Source)
	}
	if licenseOf(ev.Source) != "CC0-1.0" {
		t.Errorf("peer license %s", licenseOf(ev.Source))
	}

	// B serves its own volunteer receptions back, not its AISHub ones or A's, labelled as B labels A's.
	b.Ingest(Reception{Source: "aishub", Station: "aishub", RecvTime: now, Body: "!AIVDM,1,1,,B,14eG;u@000o<Ev:L2bEbEQah08QS,0*4A"})
	b.Ingest(Reception{Source: "station:ed25519:y", Station: "station:ed25519:y", RecvTime: now, Body: "!AIVDM,1,1,,B,14eGkT002lo<6sfL22EC<2Qn080A,0*11"})
	ev = next(t, aSub, isPeer)
	if want := "peer:" + nodeSub(keyB); ev.Source != want || ev.Station != want+"/station:ed25519:y" {
		t.Errorf("A got %s from %s", ev.Station, ev.Source)
	}

	// An approved node's socket gets only what B may re-serve: no plain subscription to B's feeds.
	ctx := t.Context()
	c, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: http.Header{nodeHeader: {nodeProof(keyA, hostOf(url), time.Now())}}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	c.Read(ctx) // welcome
	c.Write(ctx, websocket.MessageText, []byte(`{"type":"subscribe"}`))
	if _, m, err := c.Read(ctx); err != nil || !strings.Contains(string(m), "origin=local") {
		t.Errorf("plain subscribe from a peer: %s %v", m, err)
	}
}

// An unapproved node, or a server that does not know peering, is only connected to: nothing is exchanged.
func TestPeerUnapproved(t *testing.T) {
	a := testPipeline(t)
	b, _, url := peerServer(t)
	keyA := nodeKey(t)
	bSub := b.subscribe()
	defer b.unsubscribe(bSub)
	dialPeer(t, a, url, keyA)
	time.Sleep(200 * time.Millisecond)
	a.Ingest(Reception{Source: "station:ed25519:x", Station: "station:ed25519:x", RecvTime: time.Now(), Body: "!AIVDM,1,1,,A,13HOI:0P0000VOHLCnHQKwvL05Ip,0*23"})
	select {
	case ev := <-bSub.ch:
		t.Fatalf("B got %s from an unapproved node", ev.Source)
	case <-time.After(1500 * time.Millisecond):
	}

	// A proven but unapproved key has a plain client's rights: no publish, no origin=local.
	ctx := t.Context()
	c, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: http.Header{nodeHeader: {nodeProof(keyA, hostOf(url), time.Now())}}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	read := func() map[string]any {
		_, b, err := c.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		json.Unmarshal(b, &m)
		return m
	}
	if w := read(); w["role"] != "anonymous" || w["node"] != nil {
		t.Fatalf("welcome: %v", w)
	}
	c.Write(ctx, websocket.MessageText, []byte(`{"type":"subscribe","origin":"local"}`))
	if m := read(); m["type"] != "error" {
		t.Errorf("origin=local: %v", m)
	}
	c.Write(ctx, websocket.MessageText, []byte(`{"type":"publish","nmea":["!AIVDM,1,1,,A,13HOI:0P0000VOHLCnHQKwvL05Ip,0*23"]}`))
	if m := read(); m["error"] != "publish requires a token" {
		t.Errorf("publish: %v", m)
	}
}

func TestVerifyNode(t *testing.T) {
	key := nodeKey(t)
	now := time.Now()
	h := nodeProof(key, "a.example", now)
	sub, ms, err := verifyNode(h, "a.example", now)
	if err != nil || sub != nodeSub(key) {
		t.Fatalf("fresh proof: %s %v", sub, err)
	}
	if !freshProof(sub, ms) || freshProof(sub, ms) {
		t.Error("a replayed proof was fresh")
	}
	if _, _, err := verifyNode(h, "b.example", now); err == nil {
		t.Error("a proof for another node verified")
	}
	if _, _, err := verifyNode(nodeProof(key, "a.example", now.Add(-10*time.Minute)), "a.example", now); err == nil {
		t.Error("a stale proof verified")
	}
	other := nodeProof(nodeKey(t), "a.example", now)
	if _, _, err := verifyNode(h[:strings.Index(h, ".")]+other[strings.Index(other, "."):], "a.example", now); err == nil {
		t.Error("another key's signature verified")
	}

	// The reply proves the receiving node's key over this proof, and no other.
	server := nodeKey(t)
	reply := nodeReply(server, h)
	if !verifyReply(nodeSub(server), h, reply) {
		t.Error("a genuine reply did not verify")
	}
	if verifyReply(nodeSub(key), h, reply) || verifyReply(nodeSub(server), other, reply) {
		t.Error("a reply verified for another key or proof")
	}
}
