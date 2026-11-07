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
