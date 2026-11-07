package main

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"os"
	"testing"
	"time"
)

func TestEnsureSelfSignedCert(t *testing.T) {
	dir := t.TempDir()
	crt, key, err := ensureSelfSignedCert(dir)
	if err != nil {
		t.Fatalf("ensureSelfSignedCert: %v", err)
	}
	// Files were written and load as a valid key pair.
	if _, err := tls.LoadX509KeyPair(crt, key); err != nil {
		t.Fatalf("LoadX509KeyPair: %v", err)
	}
	// Second call reuses the existing certificate (no regeneration churn).
	crt2, key2, err := ensureSelfSignedCert(dir)
	if err != nil || crt2 != crt || key2 != key {
		t.Fatalf("expected reuse of existing cert, got %v %v (err=%v)", crt2, key2, err)
	}
	// The PEM parses and the cert covers localhost + the loopback + a LAN IP.
	der, _ := os.ReadFile(crt)
	block, _ := pem.Decode(der)
	if block == nil {
		t.Fatal("no PEM block in server.crt")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}
	if len(cert.IPAddresses) == 0 {
		t.Fatal("expected SANs for the machine's addresses")
	}
	found := false
	for _, ip := range cert.IPAddresses {
		if ip.IsLoopback() {
			found = true
		}
	}
	if !found {
		t.Fatal("expected a loopback SAN IP")
	}
	// A TLS server on the cert serves the UI.
	app, err := NewApp(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	srv := &http.Server{Handler: app.setupRoutes()}
	go srv.ServeTLS(ln, crt, key)
	tr := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	client := &http.Client{Transport: tr, Timeout: 5 * time.Second}
	var resp *http.Response
	deadline := time.Now().Add(6 * time.Second)
	for {
		resp, err = client.Get("https://" + ln.Addr().String() + "/")
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("https GET failed: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || len(body) == 0 {
		t.Fatalf("status=%d body len=%d", resp.StatusCode, len(body))
	}
}