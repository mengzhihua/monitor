package stream

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestClientCertIsRequiredAndVerified(t *testing.T) {
	dir := t.TempDir()
	ca, caKey := newCA(t, dir, "ca")
	serverCert := signCert(t, dir, "server", ca, caKey, true)
	clientCert := signCert(t, dir, "client", ca, caKey, false)
	otherCA, otherKey := newCA(t, dir, "other")
	otherClient := signCert(t, dir, "other-client", otherCA, otherKey, false)

	cfg, err := ServerTLS(CertFiles{CertFile: serverCert.cert, KeyFile: serverCert.key, CAFile: ca.cert, RequireClientCert: true})
	if err != nil {
		t.Fatal(err)
	}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", cfg)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 {
			http.Error(w, "unverified", http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})}
	go srv.Serve(ln)
	t.Cleanup(func() { _ = srv.Close() })

	good, err := ClientTLS(CertFiles{CertFile: clientCert.cert, KeyFile: clientCert.key, CAFile: ca.cert})
	if err != nil {
		t.Fatal(err)
	}
	if status := getStatus(t, ln.Addr().String(), good); status != http.StatusNoContent {
		t.Fatalf("trusted client: %d", status)
	}
	bad, err := ClientTLS(CertFiles{CertFile: otherClient.cert, KeyFile: otherClient.key, CAFile: ca.cert})
	if err != nil {
		t.Fatal(err)
	}
	if status := getStatus(t, ln.Addr().String(), bad); status == http.StatusNoContent {
		t.Fatal("certificate from another CA was accepted")
	}
	anon, err := ClientTLS(CertFiles{CAFile: ca.cert})
	if err != nil {
		t.Fatal(err)
	}
	if status := getStatus(t, ln.Addr().String(), anon); status == http.StatusNoContent {
		t.Fatal("missing client certificate was accepted")
	}
}

type pemPair struct{ cert, key string }

func newCA(t *testing.T, dir, name string) (pemPair, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: name},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return writePair(t, dir, name, der, key), key
}

func signCert(t *testing.T, dir, name string, ca pemPair, caKey *ecdsa.PrivateKey, server bool) pemPair {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: name},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	if server {
		tpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
		tpl.IPAddresses = []net.IP{net.ParseIP("127.0.0.1")}
	}
	caPEM, err := os.ReadFile(ca.cert)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(caPEM)
	parent, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, parent, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	return writePair(t, dir, name, der, key)
}

func writePair(t *testing.T, dir, name string, der []byte, key *ecdsa.PrivateKey) pemPair {
	t.Helper()
	certPath := filepath.Join(dir, name+".crt")
	keyPath := filepath.Join(dir, name+".key")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	return pemPair{cert: certPath, key: keyPath}
}

func getStatus(t *testing.T, addr string, cfg *tls.Config) int {
	t.Helper()
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: cfg}}
	resp, err := client.Get("https://" + addr + "/")
	if err != nil {
		return 0
	}
	resp.Body.Close()
	return resp.StatusCode
}
