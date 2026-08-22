// aiscast-key mints and inspects aiscast access tokens. The issuer private key never goes near the server.
//
//	aiscast-key issuer                       # new issuer keypair: prints kid, public key (for ISSUER_PUBKEYS), seed
//	aiscast-key new -seed <seed> -kid <kid> -sub station-42 -role feeder [-exp 8760h] [-bbox s,w,n,e]... [-cidr 203.0.113.0/24]... [-conns 2] [-rate 50] [-area 400|-1] [-mmsis 50]
//	aiscast-key inspect <token>              # claims without verification
package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"
)

type bbox [4]float64

type claims struct {
	Kid   string   `json:"kid"`
	Sub   string   `json:"sub"`
	Role  string   `json:"role"`
	Exp   int64    `json:"exp"`
	Iat   int64    `json:"iat,omitempty"`
	BBox  []bbox   `json:"bbox,omitempty"`
	CIDR  []string `json:"cidr,omitempty"`
	Conns int      `json:"conns,omitempty"`
	Rate  int      `json:"rate,omitempty"`
	Area  float64  `json:"area,omitempty"`
	MMSIs int      `json:"mmsis,omitempty"`
}

type multi []string

func (m *multi) String() string     { return strings.Join(*m, ",") }
func (m *multi) Set(s string) error { *m = append(*m, s); return nil }

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: aiscast-key issuer | new ... | inspect <token>")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "issuer":
		pk, sk, _ := ed25519.GenerateKey(nil)
		kid := time.Now().UTC().Format("2006-01")
		fmt.Printf("kid:    %s\npublic: %s\nseed:   %s\n\nISSUER_PUBKEYS=%s:%s\n", kid, b64(pk), b64(sk.Seed()), kid, b64(pk))
	case "new":
		fs := flag.NewFlagSet("new", flag.ExitOnError)
		seed := fs.String("seed", os.Getenv("ISSUER_SEED"), "issuer seed (base64url), or ISSUER_SEED")
		kid := fs.String("kid", os.Getenv("ISSUER_KID"), "issuer key id, or ISSUER_KID")
		sub := fs.String("sub", "", "subject: station id, partner name, device key")
		role := fs.String("role", "", "personal | feeder | peer | partner | admin")
		exp := fs.Duration("exp", 365*24*time.Hour, "lifetime")
		conns := fs.Int("conns", 0, "max concurrent WebSockets (0 = unlimited)")
		rate := fs.Int("rate", 0, "max messages per second per connection, excess thinned (0 = unlimited)")
		area := fs.Float64("area", 0, "max total subscribed bbox area in square degrees (0 = unlimited, -1 = MMSI subscriptions only)")
		mmsis := fs.Int("mmsis", 0, "max vessels followed by MMSI per subscription (0 = unlimited)")
		var boxes, cidrs multi
		fs.Var(&boxes, "bbox", "allowed bbox minLat,minLon,maxLat,maxLon (repeatable)")
		fs.Var(&cidrs, "cidr", "allowed source CIDR or IP (repeatable)")
		fs.Parse(os.Args[2:])
		sb, err := base64.RawURLEncoding.DecodeString(*seed)
		if err != nil || len(sb) != ed25519.SeedSize || *kid == "" || *sub == "" || *role == "" {
			fmt.Fprintln(os.Stderr, "need -seed, -kid, -sub, -role")
			os.Exit(2)
		}
		c := claims{Kid: *kid, Sub: *sub, Role: *role, Iat: time.Now().Unix(), Exp: time.Now().Add(*exp).Unix(), CIDR: cidrs, Conns: *conns, Rate: *rate, Area: *area, MMSIs: *mmsis}
		for _, b := range boxes {
			var x bbox
			if n, _ := fmt.Sscanf(b, "%f,%f,%f,%f", &x[0], &x[1], &x[2], &x[3]); n != 4 {
				fmt.Fprintln(os.Stderr, "bbox must be minLat,minLon,maxLat,maxLon")
				os.Exit(2)
			}
			c.BBox = append(c.BBox, x)
		}
		body, _ := json.Marshal(c)
		b := base64.RawURLEncoding.EncodeToString(body)
		sig := ed25519.Sign(ed25519.NewKeyFromSeed(sb), []byte("ak1."+b))
		fmt.Println("ak1." + b + "." + base64.RawURLEncoding.EncodeToString(sig))
	case "inspect":
		if len(os.Args) < 3 {
			os.Exit(2)
		}
		parts := strings.Split(os.Args[2], ".")
		if len(parts) != 3 || parts[0] != "ak1" {
			fmt.Fprintln(os.Stderr, "not an ak1 token")
			os.Exit(1)
		}
		body, err := base64.RawURLEncoding.DecodeString(parts[1])
		if err != nil {
			os.Exit(1)
		}
		var c claims
		json.Unmarshal(body, &c)
		out, _ := json.MarshalIndent(c, "", "  ")
		fmt.Println(string(out))
		if c.Exp > 0 {
			fmt.Println("expires", time.Unix(c.Exp, 0).UTC().Format(time.RFC3339))
		}
	default:
		os.Exit(2)
	}
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
