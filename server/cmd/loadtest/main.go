// loadtest opens N aisstream-style clients against a server and reports delivered message rates and server drops.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

func main() {
	url := flag.String("url", "ws://localhost:8080/v0/stream", "aiscast /v0/stream URL")
	n := flag.Int("clients", 100, "concurrent clients")
	dur := flag.Duration("duration", 20*time.Second, "test duration")
	box := flag.String("bbox", "[[[-90,-180],[90,180]]]", "BoundingBoxes JSON")
	metrics := flag.String("metrics", "", "aiscast /metrics URL to read drops from (default derived from -url)")
	flag.Parse()
	if *metrics == "" {
		*metrics = "http" + strings.TrimSuffix(strings.TrimPrefix(*url, "ws"), "/v0/stream") + "/metrics"
	}

	ctx, cancel := context.WithTimeout(context.Background(), *dur)
	defer cancel()
	var msgs, bytes, connected, failed atomic.Int64
	for i := 0; i < *n; i++ {
		go func() {
			c, _, err := websocket.Dial(ctx, *url, nil)
			if err != nil {
				failed.Add(1)
				return
			}
			defer c.CloseNow()
			c.SetReadLimit(1 << 20)
			if err := c.Write(ctx, websocket.MessageText, []byte(`{"APIKey":"loadtest","BoundingBoxes":`+*box+`}`)); err != nil {
				failed.Add(1)
				return
			}
			connected.Add(1)
			for {
				_, b, err := c.Read(ctx)
				if err != nil {
					return
				}
				msgs.Add(1)
				bytes.Add(int64(len(b)))
			}
		}()
	}
	start := time.Now()
	tick := time.NewTicker(5 * time.Second)
	for {
		select {
		case <-tick.C:
			el := time.Since(start).Seconds()
			fmt.Printf("%4.0fs clients=%d failed=%d msgs=%d (%.0f/s, %.1f/client/s) %.2f MB/s\n", el, connected.Load(), failed.Load(), msgs.Load(), float64(msgs.Load())/el, float64(msgs.Load())/el/float64(max(connected.Load(), 1)), float64(bytes.Load())/el/1e6)
		case <-ctx.Done():
			fmt.Println(serverDrops(*metrics))
			return
		}
	}
}

func serverDrops(url string) string {
	res, err := http.Get(url)
	if err != nil {
		return "metrics: " + err.Error()
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	var out []string
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, "aiscast_client_drops_total") || strings.HasPrefix(l, "aiscast_clients") || strings.HasPrefix(l, "aiscast_events_total") {
			out = append(out, l)
		}
	}
	return "server: " + strings.Join(out, "  ")
}
