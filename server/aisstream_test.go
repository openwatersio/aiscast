package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/BertoldVdb/go-ais"
)

func TestAisstreamEnvelopeMapping(t *testing.T) {
	p := testPipeline(t)
	sub := p.subscribe()
	// an envelope as aisstream emits it (the golden from the research doc)
	body := `{"Message":{"PositionReport":{"Cog":360,"CommunicationState":81982,"Latitude":55.990715,"Longitude":-3.183045,"MessageID":3,"NavigationalStatus":3,"PositionAccuracy":false,"Raim":false,"RateOfTurn":-128,"RepeatIndicator":0,"Sog":0,"Spare":0,"SpecialManoeuvreIndicator":0,"Timestamp":36,"TrueHeading":511,"UserID":250013879,"Valid":true}},"MessageType":"PositionReport","MetaData":{"MMSI":250013879,"MMSI_String":250013879,"ShipName":"DINOPOTES           ","latitude":55.990715,"longitude":-3.183045,"time_utc":"2023-10-22 22:47:36.94034384 +0000 UTC"}}`
	now := time.Date(2023, 10, 22, 22, 47, 40, 0, time.UTC)
	p.aisstreamMessage([]byte(body), now)
	p.aisstreamMessage([]byte(body), now) // duplicate
	p.aisstreamMessage([]byte(`{"MessageType":"UnknownMessage","Message":{"UnknownMessage":{}}}`), now)
	if len(sub.ch) != 1 {
		t.Fatalf("events=%d want 1 (parse_err=%d decode_fail=%d dup=%d)", len(sub.ch), p.stats.parseErr.Load(), p.stats.decodeFail.Load(), p.stats.dup.Load())
	}
	ev := <-sub.ch
	pr := ev.Packet.(ais.PositionReport)
	if ev.MMSI != 250013879 || pr.MessageID != 3 || pr.NavigationalStatus != 3 || !ev.Synthesized || ev.Source != "aisstream" {
		t.Errorf("event: %+v", ev)
	}
	if ev.Time != time.Date(2023, 10, 22, 22, 47, 36, 940343840, time.UTC) {
		t.Errorf("time_utc not used as canonical time: %v", ev.Time)
	}
	if float64(pr.Latitude) != 55.990715 || float64(pr.Longitude) != -3.183045 {
		t.Errorf("position round trip: %v %v", pr.Latitude, pr.Longitude)
	}
}

func TestVerifyAllowExpired(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	notAfter := time.Date(2026, 11, 17, 14, 4, 0, 0, time.UTC)
	cert := func(tmpl, parent *x509.Certificate) *x509.Certificate {
		if parent == nil {
			parent = tmpl
		}
		der, err := x509.CreateCertificate(rand.Reader, tmpl, parent, &key.PublicKey, key)
		if err != nil {
			t.Fatal(err)
		}
		c, _ := x509.ParseCertificate(der)
		return c
	}
	caCert := func(serial int64, name string, notAfter time.Time, parent *x509.Certificate) *x509.Certificate {
		return cert(&x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: name}, NotBefore: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC), NotAfter: notAfter, IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}, parent)
	}
	leafCert := func(serial int64, notAfter time.Time, parent *x509.Certificate) *x509.Certificate {
		return cert(&x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: "stream.aisstream.io"}, DNSNames: []string{"stream.aisstream.io"}, NotBefore: notAfter.AddDate(0, -3, 0), NotAfter: notAfter, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}, parent)
	}
	root := caCert(1, "root", notAfter.AddDate(5, 0, 0), nil)
	inter := caCert(2, "intermediate", notAfter.AddDate(1, 0, 0), root)
	shortInter := caCert(3, "short intermediate", notAfter.Add(2*24*time.Hour), root)
	leaf := leafCert(4, notAfter, inter)
	shortLeaf := leafCert(5, notAfter, shortInter)
	roots := x509.NewCertPool()
	roots.AddCert(root)
	chain := func(host string, certs ...*x509.Certificate) tls.ConnectionState {
		return tls.ConnectionState{ServerName: host, PeerCertificates: certs}
	}
	const host = "stream.aisstream.io"
	for _, c := range []struct {
		name  string
		cs    tls.ConnectionState
		roots *x509.CertPool
		now   time.Time
		ok    bool
	}{
		{"valid", chain(host, leaf, inter), roots, notAfter.Add(-time.Hour), true},
		{"expired within grace", chain(host, leaf, inter), roots, notAfter.Add(10 * 24 * time.Hour), true},
		{"expired past grace", chain(host, leaf, inter), roots, notAfter.Add(certGrace + time.Hour), false},
		{"not yet valid", chain(host, leaf, inter), roots, notAfter.AddDate(0, -4, 0), false},
		{"expired intermediate", chain(host, shortLeaf, shortInter), roots, notAfter.Add(5 * 24 * time.Hour), false},
		{"wrong host, valid", chain("evil.example", leaf, inter), roots, notAfter.Add(-time.Hour), false},
		{"wrong host, expired", chain("evil.example", leaf, inter), roots, notAfter.Add(time.Hour), false},
		{"no server name", chain("", leaf, inter), roots, notAfter.Add(-time.Hour), false},
		{"untrusted root", chain(host, leaf, inter), x509.NewCertPool(), notAfter.Add(time.Hour), false},
		{"no certificates", chain(host), roots, notAfter, false},
	} {
		if err := verifyAllowExpired(c.cs, c.roots, c.now); (err == nil) != c.ok {
			t.Errorf("%s: err = %v, want ok=%v", c.name, err, c.ok)
		}
	}
}

// aisstreamHTTP skips Go's own verification, so it must reach verifyAllowExpired: a self-signed server is refused.
func TestAisstreamHTTPVerifies(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	resp, err := aisstreamHTTP.Get(strings.Replace(srv.URL, "127.0.0.1", "localhost", 1))
	if err == nil {
		resp.Body.Close()
		t.Fatal("self-signed certificate accepted")
	}
	if !strings.Contains(err.Error(), "x509:") {
		t.Fatalf("err = %v, want a certificate error", err)
	}
}
