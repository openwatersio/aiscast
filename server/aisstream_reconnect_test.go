package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestReconnectDelayResetsAfterAWorkingSession(t *testing.T) {
	if got := reconnectDelay(5*time.Minute, 2*time.Hour); got != 5*time.Second {
		t.Errorf("after a long session: %s, want 5s", got)
	}
	if got := reconnectDelay(40*time.Second, 3*time.Second); got != 40*time.Second {
		t.Errorf("after a failed attempt: %s, want the backoff kept", got)
	}
}

func TestAisstreamSubscriptionsOnePerBox(t *testing.T) {
	subs, err := aisstreamSubscriptions(aisstreamDefaultBoxes)
	want := []string{"[[[-90,-180],[90,4.5]]]", "[[[-90,4.5],[90,180]]]"}
	if err != nil || !slices.Equal(subs, want) {
		t.Fatalf("default: %q, %v; want %q", subs, err, want)
	}
	if subs, err := aisstreamSubscriptions("[[[50,3],[53,5]]]"); err != nil || len(subs) != 1 {
		t.Errorf("one box: %q, %v", subs, err)
	}
	// aisstream closes a third connection, so boxes past the second share the second connection
	subs, err = aisstreamSubscriptions("[[[0,0],[1,1]],[[1,1],[2,2]],[[2,2],[3,3]]]")
	if want := []string{"[[[0,0],[1,1]]]", "[[[1,1],[2,2]],[[2,2],[3,3]]]"}; err != nil || !slices.Equal(subs, want) {
		t.Errorf("three boxes: %q, %v; want %q", subs, err, want)
	}
	for _, bad := range []string{
		"", "[]", "[[[-90,-180],[90,180]]", `{"lat":1}`,
		"[[[60,11]]]", "[[[60,11],[58,9],[1,1]]]", "[[[60,11,5],[58,9]]]", "[null]", // not two [lat, lon] corners
		"[[[91,0],[0,0]]]", "[[[0,-181],[0,0]]]",
	} {
		if _, err := aisstreamSubscriptions(bad); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
}

func TestAisstreamDialsTakeTurns(t *testing.T) {
	const gap = 200 * time.Millisecond
	turns := make(chan time.Time, 2)
	for range 2 {
		go func() { waitAisstreamDialTurn(gap); turns <- time.Now() }()
	}
	a, b := <-turns, <-turns
	if d := b.Sub(a); d < gap-20*time.Millisecond {
		t.Fatalf("two concurrent dials %s apart, want at least %s", d, gap)
	}
}

func TestFailedAttemptAfterLongBlockKeepsBackoff(t *testing.T) {
	// a dial or subscribe that blocked for minutes and then failed was never established
	if got := reconnectDelay(2*time.Minute, sessionLength(time.Time{})); got != 2*time.Minute {
		t.Fatalf("a session never established reset the backoff to %s", got)
	}
	if got := reconnectDelay(2*time.Minute, sessionLength(time.Now().Add(-3*time.Minute))); got != 5*time.Second {
		t.Fatalf("an established session of 3 minutes kept the backoff at %s", got)
	}
}

// aisstreamServer accepts a session, reads the subscription, sends frames, then either drops the socket
// without a close frame, as aisstream does, or holds it open in silence.
func aisstreamServer(t *testing.T, hold bool, frames ...string) string {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		c.Read(r.Context())
		for _, f := range frames {
			c.Write(r.Context(), websocket.MessageText, []byte(f))
		}
		if hold {
			c.Read(r.Context()) // until the client gives up
		}
	}))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

func TestAisstreamMetricsPerConnection(t *testing.T) {
	p := testPipeline(t)
	gap, silence := aisstreamDialGap, aisstreamSilence
	aisstreamDialGap, aisstreamSilence = 0, 200*time.Millisecond
	t.Cleanup(func() { aisstreamDialGap, aisstreamSilence = gap, silence })
	const box = "[[[1,1],[2,2]]]"
	conn := newAisstreamConn(box)
	t.Cleanup(func() { aisstreamConns.Delete(box) })
	stale := time.Now().Add(-time.Hour).UnixNano()
	confirmation := `{"MessageType":"SubscriptionConfirmation","Message":{}}`
	position := `{"Message":{"PositionReport":{"Cog":360,"Latitude":55.990715,"Longitude":-3.183045,"MessageID":1,"NavigationalStatus":0,"RateOfTurn":-128,"TrueHeading":511,"UserID":250013879,"Valid":true}},"MessageType":"PositionReport","MetaData":{"MMSI":250013879,"time_utc":"2023-10-22 22:47:36.94034384 +0000 UTC"}}`

	// aisstream confirms every subscription, so a connection cut right after it must still look silent
	conn.last.Store(stale)
	if connected, err := aisstreamSession(p, aisstreamServer(t, false, confirmation), "key", box, conn); connected.IsZero() || err == nil {
		t.Fatalf("session: connected %v, err %v", connected, err)
	}
	if conn.drops.Load() != 1 || conn.last.Load() != stale {
		t.Errorf("after a confirmation and a drop: drops %d, last refreshed %v", conn.drops.Load(), conn.last.Load() != stale)
	}

	aisstreamSession(p, aisstreamServer(t, false, confirmation, position), "key", box, conn)
	if conn.drops.Load() != 2 || conn.last.Load() == stale {
		t.Errorf("after a position and a drop: drops %d, last refreshed %v", conn.drops.Load(), conn.last.Load() != stale)
	}

	// a rejected key ends the session with an error frame, which counts
	if _, err := aisstreamSession(p, aisstreamServer(t, true, `{"error":"Api Key Is Not Valid"}`), "key", box, conn); err == nil || conn.drops.Load() != 3 {
		t.Errorf("an error frame: drops %d, err %v", conn.drops.Load(), err)
	}

	// a session we end on silence is an aisstream outage, not a drop
	aisstreamSession(p, aisstreamServer(t, true, confirmation), "key", box, conn)
	if conn.drops.Load() != 3 {
		t.Errorf("a silence timeout counted as a drop: %d", conn.drops.Load())
	}
	if _, err := aisstreamSession(p, "ws://127.0.0.1:1", "key", box, conn); err == nil || conn.drops.Load() != 3 {
		t.Errorf("a failed dial counted as a drop: %d, err %v", conn.drops.Load(), err)
	}

	var b strings.Builder
	writeAisstreamMetrics(&b)
	if !strings.Contains(b.String(), "aiscast_aisstream_drops_total{bbox=\""+box+"\"} 3\n") {
		t.Errorf("metrics lack the drops:\n%s", b.String())
	}
	var age float64
	if _, after, ok := strings.Cut(b.String(), "aiscast_aisstream_last_age_seconds{bbox=\""+box+"\"} "); !ok {
		t.Errorf("metrics lack the age:\n%s", b.String())
	} else if _, err := fmt.Sscan(after, &age); err != nil || age > 30 {
		t.Errorf("age %v (%v) after a position just now", age, err)
	}
	if newAisstreamConn(box) != conn {
		t.Error("a second connection with the same boxes got its own entry, hiding the first from /metrics")
	}
}
