package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// Every aiscast node is one peer in a network. On start it connects to each node below, proving its node key,
// and holds the connection open. Once the other node's operator approves that key, the connection carries
// each side's own volunteer receptions to the other.
//
// The list is compiled in, with no setting that removes or replaces an entry (ALLOW_ANON stops dialing, but it also
// turns off tokens, so no public node runs with it): leaving the network is a change to this file, and AGPL-3.0 §13
// asks a hosted copy to offer its users the source of that change.
type peer struct{ Name, URL string }

var peers = []peer{{Name: "openwaters", URL: "wss://ais.openwaters.io/v1/stream"}}

// peerSource labels what arrives from a peer, as peer:<node key>: counted, archived, and served, never re-shared.
const peerSource = "peer"

// nodeHeader carries a node's proof of its key on every connection: <pubkey>.<unix ms>.<sig>, the signature
// over nodeProofMessage. A proof per connection, not a token, so a key's public half is never enough to act as it.
const nodeHeader = "Aiscast-Node"

// nodeAgent names the software on every peer connection, so its operator can tell nodes from other clients.
var nodeAgent = "aiscast/" + buildRevision

// peerable: what a node shares with its peers is its own volunteer receptions, the AISHub rule, less own-ship
// reports. Feeds and aggregates (Kystverket, Digitraffic, AISHub, aisstream) every node can pull itself, and some
// may not be re-served; peer data is not a volunteer source, so it travels one hop and never loops.
func peerable(ev *Event) bool {
	return feedable(ev) && !ev.Own && !(ev.LowTrust && !ev.Corroborated)
}

// loadNodeKey reads the node's ed25519 seed, creating it on first start. The key is the node's identity on
// every peer: the approval that names it, and the source its receptions carry there.
func loadNodeKey(path string) (ed25519.PrivateKey, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		seed := make([]byte, ed25519.SeedSize)
		rand.Read(seed)
		b = []byte(base64.RawURLEncoding.EncodeToString(seed))
		if err = writeSynced(path+".tmp", b); err == nil {
			err = os.Rename(path+".tmp", path) // whole or absent: a crash mid-write must not leave a seed that never loads
		}
	}
	if err != nil {
		return nil, err
	}
	seed, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(string(b)))
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("%s: not a base64url ed25519 seed", path)
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

func writeSynced(path string, b []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(b)
	if err == nil {
		err = f.Sync()
	}
	return errors.Join(err, f.Close())
}

func nodeSub(key ed25519.PrivateKey) string {
	return "ed25519:" + base64.RawURLEncoding.EncodeToString(key.Public().(ed25519.PublicKey))
}

// parseApproved reads PEERS_APPROVED: the comma-separated keys of the nodes this one exchanges receptions with.
func parseApproved(s string) map[string]bool {
	m := map[string]bool{}
	for _, sub := range strings.Split(s, ",") {
		if sub = strings.TrimSpace(sub); sub != "" {
			m[sub] = true
		}
	}
	return m
}

// nodeProofMessage is what a node signs to connect: its key, the host it dials, and the time. Naming the host
// keeps a proof sent to one node from opening a connection to another.
func nodeProofMessage(pub, host string, ms int64) []byte {
	return []byte("aiscast-node\n" + pub + "\n" + host + "\n" + strconv.FormatInt(ms, 10))
}

func nodeProof(key ed25519.PrivateKey, host string, now time.Time) string {
	pub := base64.RawURLEncoding.EncodeToString(key.Public().(ed25519.PublicKey))
	ms := now.UnixMilli()
	sig := ed25519.Sign(key, nodeProofMessage(pub, host, ms))
	return pub + "." + strconv.FormatInt(ms, 10) + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// nodeReply is what the receiving node signs over the dialer's proof, so the dialer knows the key it labels the
// connection's receptions with is the one at the other end.
func nodeReply(key ed25519.PrivateKey, proof string) string {
	return base64.RawURLEncoding.EncodeToString(ed25519.Sign(key, []byte("aiscast-node-reply\n"+proof)))
}

func verifyReply(node, proof, sig string) bool {
	pk, err1 := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(node, "ed25519:"))
	b, err2 := base64.RawURLEncoding.DecodeString(sig)
	return err1 == nil && err2 == nil && len(pk) == ed25519.PublicKeySize && strings.HasPrefix(node, "ed25519:") &&
		ed25519.Verify(pk, []byte("aiscast-node-reply\n"+proof), b)
}

// nodeProofs remembers each approved key's last accepted proof time, so a captured header cannot open a second
// connection. Only approved keys are kept, so its size is the approval list's.
var nodeProofs = struct {
	sync.Mutex
	last map[string]int64
}{last: map[string]int64{}}

func freshProof(sub string, ms int64) bool {
	nodeProofs.Lock()
	defer nodeProofs.Unlock()
	if ms <= nodeProofs.last[sub] {
		return false
	}
	nodeProofs.last[sub] = ms
	return true
}

// verifyNode checks a nodeHeader value sent to host, returning the node's key as a sub and the proof's time.
func verifyNode(h, host string, now time.Time) (string, int64, error) {
	parts := strings.Split(h, ".")
	if len(parts) != 3 {
		return "", 0, errors.New("malformed node proof")
	}
	pk, err1 := base64.RawURLEncoding.DecodeString(parts[0])
	ms, err2 := strconv.ParseInt(parts[1], 10, 64)
	sig, err3 := base64.RawURLEncoding.DecodeString(parts[2])
	if err1 != nil || err2 != nil || err3 != nil || len(pk) != ed25519.PublicKeySize ||
		!ed25519.Verify(pk, nodeProofMessage(parts[0], host, ms), sig) {
		return "", 0, errors.New("node proof does not verify")
	}
	if d := now.Sub(time.UnixMilli(ms)); d > mintSkew || d < -mintSkew {
		return "", 0, errors.New("node proof must be within 5 minutes of the server clock")
	}
	return "ed25519:" + parts[0], ms, nil
}

var tagStation = strings.NewReplacer(",", "_", "*", "_", `\`, "_")

// tagLine replaces a sentence's TAG block with one naming its station and time, so the peer keeps each
// station apart (as <source>/<station>) and takes the event's own time.
func tagLine(station string, t time.Time, line string) string {
	if strings.HasPrefix(line, `\`) {
		if i := strings.IndexByte(line[1:], '\\'); i >= 0 {
			line = line[i+2:]
		}
	}
	tag := "s:" + tagStation.Replace(station) + ",c:" + strconv.FormatInt(t.UnixMilli(), 10)
	var sum byte
	for i := 0; i < len(tag); i++ {
		sum ^= tag[i]
	}
	return fmt.Sprintf(`\%s*%02X\%s`, tag, sum, line)
}

var errSelf = errors.New("this node")

// runPeer holds one peer connection for the life of the process, reconnecting with backoff. It ends only when
// the peer says the connection is to itself, which is how a node skips its own entry in peers.
func runPeer(p *Pipeline, pr peer, key ed25519.PrivateKey) {
	backoff := time.Second
	for {
		start := time.Now()
		err := p.peerOnce(context.Background(), pr, key)
		if errors.Is(err, errSelf) {
			log.Printf("peer %s: %s is this node; not peering with itself", pr.Name, pr.URL)
			return
		}
		if time.Since(start) > time.Minute {
			backoff = time.Second
		}
		log.Printf("peer %s: %v (retry in %s)", pr.Name, err, backoff)
		time.Sleep(backoff)
		backoff = min(backoff*2, 5*time.Minute)
	}
}

// peerFrame is any frame a peer sends: welcome, ack, error, or event.
type peerFrame struct {
	Type    string   `json:"type"`
	Error   string   `json:"error"`
	Role    string   `json:"role"`
	Node    string   `json:"node"`
	NodeSig string   `json:"node_sig"`
	Station string   `json:"station"`
	Time    string   `json:"time"`
	NMEA    []string `json:"nmea"`
}

// peerOnce holds one connection until it fails. Until the peer welcomes this node in the peer role, it only
// waits, which is also how a peer running an older aiscast is handled: that one never grants the role.
func (p *Pipeline) peerOnce(ctx context.Context, pr peer, key ed25519.PrivateKey) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	u, err := url.Parse(pr.URL)
	if err != nil {
		return err
	}
	proof := nodeProof(key, u.Host, time.Now())
	h := http.Header{"User-Agent": {nodeAgent}, nodeHeader: {proof}}
	dctx, dcancel := context.WithTimeout(ctx, 30*time.Second)
	c, _, err := websocket.Dial(dctx, pr.URL, &websocket.DialOptions{HTTPHeader: h})
	dcancel()
	if err != nil {
		return err
	}
	defer c.CloseNow()
	c.SetReadLimit(1 << 20)
	read := func() (peerFrame, error) {
		var f peerFrame
		_, b, err := c.Read(ctx)
		if err == nil {
			err = json.Unmarshal(b, &f)
		}
		if err == nil && f.Type == "error" {
			if f.Error == errSelf.Error() {
				return f, errSelf
			}
			err = errors.New(f.Error)
		}
		return f, err
	}
	go p.pingLoop(ctx, c, cancel) // a silently dropped link, or a peer that never welcomes, ends this connection
	w, err := read()
	if err != nil {
		return err
	}
	errc := make(chan error, 1)
	if w.Role != "peer" || !verifyReply(w.Node, proof, w.NodeSig) {
		log.Printf("peer %s: connected; waiting for its operator to approve %s", pr.Name, p.nodeSub)
		go func() {
			for {
				if _, err := read(); err != nil {
					errc <- err
					return
				}
			}
		}()
		return <-errc
	}
	log.Printf("peer %s: connected as %s", pr.Name, w.Node)
	if err := wsWriteJSON(ctx, c, v1Frame{Type: "subscribe", Origin: "local"}); err != nil {
		return err
	}

	// What the peer serves back is its own receptions, labelled with its key as it labels ours.
	src := peerSource + ":" + w.Node
	go func() {
		for {
			f, err := read()
			if err != nil {
				errc <- err
				return
			}
			if f.Type != "event" || len(f.NMEA) == 0 {
				continue
			}
			t, _ := time.Parse(time.RFC3339Nano, f.Time)
			for _, line := range f.NMEA {
				p.Ingest(Reception{Source: src, Station: src, RecvTime: time.Now(), Body: tagLine(f.Station, t, line)})
			}
		}
	}()

	sub := p.tap()
	defer p.untap(sub)
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	var batch []string
	for {
		select {
		case err := <-errc:
			return err
		case <-ctx.Done():
			return errors.New("no answer to ping")
		case ev := <-sub.ch:
			if sub.overflow.Load() {
				return errors.New("fell behind the local stream")
			}
			if !peerable(ev) {
				continue
			}
			for _, line := range ev.Sentences {
				batch = append(batch, tagLine(ev.Station, ev.Time, line))
			}
			if len(batch) < maxPublishFrame/2 {
				continue
			}
		case <-tick.C:
			if len(batch) == 0 {
				continue
			}
		}
		// Replay: each sentence carries its own reception time, which a backlog or a stalled link leaves past maxSkew.
		if err := wsWriteJSON(ctx, c, v1Frame{Type: "publish", NMEA: batch, Replay: true}); err != nil {
			return err
		}
		batch = nil
	}
}
