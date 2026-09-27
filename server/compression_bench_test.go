package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BertoldVdb/go-ais"
	"github.com/coder/websocket"
)

// v1Frames renders n varied /v1 event frames the way the stream sends them: many vessels, the upstream
// mix of rebuilt sources, positions moving between reports.
func v1Frames(tb testing.TB, n int) [][]byte {
	p := testPipeline(nil)
	sub := p.subscribe()
	r := rand.New(rand.NewPCG(7, 8))
	sources := []string{"aishub", "aishub", "aishub", "barentswatch", "digitraffic"}
	now := time.Now()
	var out [][]byte
	for i := 0; len(out) < n; i++ {
		mmsi := uint32(230000000 + r.IntN(2000))
		at := now.Add(time.Duration(i) * 50 * time.Millisecond)
		src := sources[i%len(sources)]
		p.ingestPacket(src, src, at, at, ais.PositionReport{Header: ais.Header{MessageID: 1, UserID: mmsi}, Valid: true,
			Latitude: ais.FieldLatLonFine(55 + r.Float64()*10), Longitude: ais.FieldLatLonFine(5 + r.Float64()*20),
			Sog: ais.Field10(r.Float64() * 20), Cog: ais.Field10(r.Float64() * 359), TrueHeading: uint16(r.IntN(360)),
			NavigationalStatus: uint8(r.IntN(9)), RateOfTurn: -128, Timestamp: uint8(at.Second())})
		for len(sub.ch) > 0 {
			b, _ := json.Marshal(renderV1(<-sub.ch))
			out = append(out, b)
		}
	}
	return out[:n]
}

// countingListener counts the bytes the server writes, which is what leaves the box.
type countingListener struct {
	net.Listener
	written atomic.Int64
}

type countingConn struct {
	net.Conn
	n *atomic.Int64
}

func (l *countingListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	return countingConn{c, &l.written}, err
}

func (c countingConn) Write(b []byte) (int, error) {
	n, err := c.Conn.Write(b)
	c.n.Add(int64(n))
	return n, err
}

// benchCompression times the server side of one compressed stream: frames are written through a real
// accepted socket, and the client reads and discards raw bytes, so decompression never counts.
func benchCompression(b *testing.B, mode websocket.CompressionMode, offer string) {
	frames := v1Frames(b, 4000)
	var raw int64
	for _, f := range frames {
		raw += int64(len(f))
	}
	next := make(chan []byte)
	done := make(chan struct{})
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: mode})
		if err != nil {
			b.Error(err)
			return
		}
		defer c.CloseNow()
		for f := range next {
			if err := c.Write(r.Context(), websocket.MessageText, f); err != nil {
				b.Error(err)
				return
			}
		}
		close(done)
	}))
	ln := &countingListener{Listener: srv.Listener}
	srv.Listener = ln
	srv.Start()
	defer srv.Close()

	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		b.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "GET / HTTP/1.1\r\nHost: x\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Version: 13\r\n"+
		"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Extensions: %s\r\n\r\n", offer)
	br := bufio.NewReader(conn)
	res, err := http.ReadResponse(br, nil)
	if err != nil || res.StatusCode != http.StatusSwitchingProtocols {
		b.Fatalf("handshake: %v %v", res, err)
	}
	b.Logf("negotiated: %s", res.Header.Get("Sec-WebSocket-Extensions"))
	go io.Copy(io.Discard, br)

	start := ln.written.Load()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		next <- frames[i%len(frames)]
	}
	b.StopTimer()
	close(next)
	<-done
	wire := float64(ln.written.Load() - start)
	b.ReportMetric(wire/float64(b.N), "wire-B/frame")
	b.ReportMetric(float64(raw)/float64(len(frames))/(wire/float64(b.N)), "ratio")
}

// The client offers context takeover, as browsers do, so the server's configured mode decides.
const clientOffer = "permessage-deflate; client_max_window_bits"

func BenchmarkCompressionContextTakeover(b *testing.B) {
	benchCompression(b, websocket.CompressionContextTakeover, clientOffer)
}

func BenchmarkCompressionNoContextTakeover(b *testing.B) {
	benchCompression(b, websocket.CompressionNoContextTakeover, clientOffer)
}

func BenchmarkCompressionDisabled(b *testing.B) {
	benchCompression(b, websocket.CompressionDisabled, clientOffer)
}
