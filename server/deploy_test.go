package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The Cloudsmith outage of 2026-10-09 as apt reported it on the box, which every deploy failed on.
const cloudsmith402 = `Err:2 https://dl.cloudsmith.io/public/caddy/stable/deb/debian any-version InRelease
  402  Payment Required [IP: 2600:9000:2375:8600:e:f4d2:20c0:93a1 443]
E: Failed to fetch https://dl.cloudsmith.io/public/caddy/stable/deb/debian/dists/any-version/InRelease  402  Payment Required [IP: 2600:9000:2375:8600:e:f4d2:20c0:93a1 443]
E: The repository 'https://dl.cloudsmith.io/public/caddy/stable/deb/debian any-version InRelease' is no longer signed.
`

// Grafana's repository answering 503, in apt's words; its source names no path after the host.
const grafana503 = `E: Failed to fetch https://apt.grafana.com/dists/stable/InRelease  503  Service Unavailable [IP: 104.18.0.1 443]
E: The repository 'https://apt.grafana.com stable InRelease' is no longer signed.
`

// TestAptUpdate runs deploy/apt-update.sh, which apply.sh sources on the box, against apt's output with
// apt-get, dpkg-query and clickhouse-server stood in for.
func TestAptUpdate(t *testing.T) {
	// Opened here as well as by sh, so the test cache sees the script change.
	if _, err := os.ReadFile("deploy/apt-update.sh"); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name, out string
		code      string
		missing   string // a package dpkg-query reports removed
		noCH      bool   // no clickhouse-server on the box
		ok        bool
		installs  bool
	}{
		{name: "refresh", out: "Hit:1 http://archive.ubuntu.com/ubuntu noble InRelease\n", code: "0", ok: true, installs: true},
		{name: "a repository refuses, every package installed", out: cloudsmith402, code: "100", ok: true},
		{name: "a repository refuses, a package missing", out: cloudsmith402, code: "100", missing: "caddy"},
		{name: "a repository refuses, ClickHouse missing", out: cloudsmith402, code: "100", noCH: true},
		{name: "a repository is down", out: grafana503, code: "100", ok: true},
		{name: "two repositories refuse", out: cloudsmith402 + grafana503, code: "100", ok: true},
		{name: "a repository forbids", code: "100",
			out: "E: Failed to fetch https://apt.grafana.com/dists/stable/InRelease  403  Forbidden [IP: 104.18.0.1 443]\nE: The repository 'https://apt.grafana.com stable InRelease' is no longer signed.\n"},
		{name: "unsigned without a refusal", code: "100",
			out: cloudsmith402 + "E: The repository 'https://apt.grafana.com stable InRelease' is no longer signed.\n"},
		{name: "a key apt cannot verify", code: "100",
			out: "W: GPG error: https://apt.grafana.com stable InRelease: NO_PUBKEY 963FA27710458545\nE: The repository 'https://apt.grafana.com stable InRelease' is not signed.\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			bin := t.TempDir()
			stub := func(name, body string) {
				if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			stub("apt-get", `if [ "$1" = install ]; then echo INSTALLED; exit 0; fi; printf '%s' "$APT_OUT"; exit "$APT_CODE"`+"\n")
			stub("dpkg-query", `eval p=\${$#}; if [ "$p" = "$MISSING" ]; then printf 'rc '; else printf 'ii '; fi`+"\n")
			if !c.noCH {
				stub("clickhouse-server", "")
			}
			cmd := exec.Command("sh", "-c", `set -eu; . ./deploy/apt-update.sh; echo REACHED`)
			cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "APT_OUT="+c.out, "APT_CODE="+c.code, "MISSING="+c.missing)
			out, err := cmd.CombinedOutput()
			got := string(out)
			if c.ok != (err == nil && strings.Contains(got, "REACHED")) {
				t.Fatalf("ok %v: %v\n%s", c.ok, err, got)
			}
			if c.installs != strings.Contains(got, "INSTALLED") {
				t.Errorf("installs %v:\n%s", c.installs, got)
			}
			if refused := c.ok && !c.installs; refused != strings.Contains(got, "::warning title=apt::") {
				t.Errorf("warning %v:\n%s", refused, got)
			}
		})
	}
}
