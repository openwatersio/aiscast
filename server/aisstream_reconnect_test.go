package main

import (
	"slices"
	"testing"
	"time"
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
