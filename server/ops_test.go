package main

import (
	"net/http/httptest"
	"testing"
)

func TestClientIP(t *testing.T) {
	cases := []struct{ peer, xff, want string }{
		{"127.0.0.1:1234", "203.0.113.9", "203.0.113.9"},
		{"127.0.0.1:1234", "10.0.0.1, 203.0.113.9", "203.0.113.9"},
		{"[::1]:1234", "203.0.113.9", "203.0.113.9"},
		{"198.51.100.7:1234", "203.0.113.9", "198.51.100.7"}, // a public peer cannot name its own address
		{"127.0.0.1:1234", "", "127.0.0.1"},
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = c.peer
		if c.xff != "" {
			r.Header.Set("X-Forwarded-For", c.xff)
		}
		if got := clientIP(r); got != c.want {
			t.Errorf("peer %s xff %q: got %s want %s", c.peer, c.xff, got, c.want)
		}
	}
}
