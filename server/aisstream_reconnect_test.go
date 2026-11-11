package main

import (
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

func TestFailedAttemptAfterLongBlockKeepsBackoff(t *testing.T) {
	// a dial or subscribe that blocked for minutes and then failed was never established
	if got := reconnectDelay(2*time.Minute, sessionLength(time.Time{})); got != 2*time.Minute {
		t.Fatalf("a session never established reset the backoff to %s", got)
	}
	if got := reconnectDelay(2*time.Minute, sessionLength(time.Now().Add(-3*time.Minute))); got != 5*time.Second {
		t.Fatalf("an established session of 3 minutes kept the backoff at %s", got)
	}
}
