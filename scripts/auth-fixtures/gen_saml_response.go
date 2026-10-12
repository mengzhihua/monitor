//go:build ignore

// Generate a signed SAML Assertion + PEM certificate for the local IdP fixture.
//
//	cd core && go run ../scripts/auth-fixtures/gen_saml_response.go \
//	  -out ../scripts/auth-fixtures/signed-assertion.xml \
//	  -cert ../scripts/auth-fixtures/idp.pem
package main

import (
	"crypto/x509"
	"encoding/pem"
	"flag"
	"fmt"
	"os"

	"github.com/beevik/etree"
	dsig "github.com/russellhaering/goxmldsig"
)

func main() {
	out := flag.String("out", "signed-assertion.xml", "signed Assertion XML path")
	certOut := flag.String("cert", "idp.pem", "IdP certificate PEM path (for web.saml.idp_cert)")
	nameID := flag.String("name", "fixture-user@example.com", "NameID")
	issuer := flag.String("issuer", "http://127.0.0.1:18080/metadata", "Assertion Issuer / entity ID")
	flag.Parse()

	ks := dsig.RandomKeyStoreForTest()
	_, certDER, err := ks.GetKeyPair()
	if err != nil {
		fail(err)
	}
	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		fail(err)
	}
	pemCert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})
	if err := os.WriteFile(*certOut, pemCert, 0o644); err != nil {
		fail(err)
	}

	doc := etree.NewDocument()
	xml := fmt.Sprintf(`<Assertion xmlns="urn:oasis:names:tc:SAML:2.0:assertion" ID="_fixture1"><Issuer>%s</Issuer><Subject><NameID>%s</NameID></Subject></Assertion>`, *issuer, *nameID)
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
	fmt.Printf("wrote %s and %s\n", *out, *certOut)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
