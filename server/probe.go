package main

import (
	"bytes"
	"context"
	"log"
	"net"
	"time"

	"github.com/coder/websocket"
)

// The probe is a loopback /v1/stream subscriber. /health fails when it has received nothing for upstreamSilence,
// so a server that ingests fine but delivers nothing to subscribers (the aisstream failure mode) reads as down.
// Anonymous tier, so the boxes must stay under anonArea; Oslofjord/Skagerrak and the Gulf of Finland always have traffic.
var probeBoxes = []bbox{{57.5, 7, 60, 12}, {59, 21, 60.8, 30}}

func probeURL(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil || host == "" {
		host = "127.0.0.1"
	}
	return "ws://" + net.JoinHostPort(host, port) + "/v1/stream"
}

func (p *Pipeline) runProbe(url string) {
	p.probeLast.Store(time.Now().Unix())
	for {
		time.Sleep(10 * time.Second) // let the listener come up; reconnects stay under wsConnectLimit
		p.probeOnce(url)
	}
}

func (p *Pipeline) probeOnce(url string) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		log.Printf("probe: %v", err)
		return
	}
	defer c.CloseNow()
	if err := wsWriteJSON(ctx, c, v1Frame{Type: "subscribe", BBox: probeBoxes}); err != nil {
		return
	}
	for {
		_, data, err := c.Read(ctx)
		if err != nil {
			log.Printf("probe: %v", err)
			return
		}
		if bytes.Contains(data, []byte(`"type":"event"`)) {
			p.probeLast.Store(time.Now().Unix())
		}
	}
}
