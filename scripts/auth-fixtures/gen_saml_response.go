//go:build ignore

// Generate a signed SAML Assertion + PEM certificate for the local IdP fixture.
//
//	cd core && go run ../scripts/auth-fixtures/gen_saml_response.go \
//	  -out ../scripts/auth-fixtures/signed-assertion.xml \
//	  -cert ../scripts/auth-fixtures/idp.pem
//
// Reuse the same IdP key for a second Assertion ID:
//
//	go run ../scripts/auth-fixtures/gen_saml_response.go \
//	  -out signed2.xml -cert idp.pem -key idp.pem.key -id _fixture2
package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"flag"
	"fmt"
	"math/big"
	"os"
	"time"

	"github.com/beevik/etree"
	dsig "github.com/russellhaering/goxmldsig"
)

type memKeyStore struct {
	key  *rsa.PrivateKey
	cert []byte
}

func (m *memKeyStore) GetKeyPair() (*rsa.PrivateKey, []byte, error) { return m.key, m.cert, nil }

func main() {
	out := flag.String("out", "signed-assertion.xml", "signed Assertion XML path")
	certOut := flag.String("cert", "idp.pem", "IdP certificate PEM path (for web.saml.idp_cert)")
	keyOut := flag.String("key", "", "PKCS#8 private key path (default: <cert>.key); reuse with -key to sign another ID")
	nameID := flag.String("name", "fixture-user@example.com", "NameID")
	issuer := flag.String("issuer", "http://127.0.0.1:18080/metadata", "Assertion Issuer / entity ID")
	assertID := flag.String("id", "_fixture1", "Assertion ID (must be unique per login)")
	flag.Parse()
	if *keyOut == "" {
		*keyOut = *certOut + ".key"
	}

	var ks *memKeyStore
	if raw, err := os.ReadFile(*keyOut); err == nil {
		block, _ := pem.Decode(raw)
		if block == nil {
			fail(fmt.Errorf("bad key pem"))
		}
		parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			fail(err)
		}
		key, ok := parsed.(*rsa.PrivateKey)
		if !ok {
			fail(fmt.Errorf("key is not RSA"))
		}
		certRaw, err := os.ReadFile(*certOut)
		if err != nil {
			fail(err)
		}
		cblock, _ := pem.Decode(certRaw)
		if cblock == nil {
			fail(fmt.Errorf("bad cert pem"))
		}
		ks = &memKeyStore{key: key, cert: cblock.Bytes}
	} else {
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			fail(err)
		}
		tmpl := &x509.Certificate{
			SerialNumber: big.NewInt(1),
			Subject:      pkix.Name{CommonName: "monitor-saml-fixture"},
			NotBefore:    time.Now().Add(-time.Hour),
			NotAfter:     time.Now().Add(24 * time.Hour),
			KeyUsage:     x509.KeyUsageDigitalSignature,
		}
		der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
		if err != nil {
			fail(err)
		}
		if err := os.WriteFile(*certOut, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
			fail(err)
		}
		pkcs8, err := x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			fail(err)
		}
		if err := os.WriteFile(*keyOut, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8}), 0o600); err != nil {
			fail(err)
		}
		ks = &memKeyStore{key: key, cert: der}
	}

	doc := etree.NewDocument()
	xml := fmt.Sprintf(`<Assertion xmlns="urn:oasis:names:tc:SAML:2.0:assertion" ID="%s"><Issuer>%s</Issuer><Subject><NameID>%s</NameID></Subject></Assertion>`, *assertID, *issuer, *nameID)
	if err := doc.ReadFromString(xml); err != nil {
		fail(err)
	}
	signed, err := dsig.NewDefaultSigningContext(ks).SignEnveloped(doc.Root())
	if err != nil {
		fail(err)
	}
	outDoc := etree.NewDocument()
	outDoc.SetRoot(signed)
	body, err := outDoc.WriteToBytes()
	if err != nil {
		fail(err)
	}
	if err := os.WriteFile(*out, body, 0o644); err != nil {
		fail(err)
	}
	fmt.Printf("wrote %s and %s (key %s)\n", *out, *certOut, *keyOut)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
