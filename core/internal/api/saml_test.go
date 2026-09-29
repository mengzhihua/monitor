package api

import (
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/beevik/etree"
	dsig "github.com/russellhaering/goxmldsig"
)

func TestSAMLLoginAndSignedAssertion(t *testing.T) {
	ks := dsig.RandomKeyStoreForTest()
	_, certDER, err := ks.GetKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		t.Fatal(err)
	}
	pemCert := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}))
	doc := etree.NewDocument()
	if err := doc.ReadFromString(`<Assertion xmlns="urn:oasis:names:tc:SAML:2.0:assertion" ID="_a1"><Issuer>idp</Issuer><Subject><NameID>ada@example.com</NameID></Subject></Assertion>`); err != nil {
		t.Fatal(err)
	}
	signed, err := dsig.NewDefaultSigningContext(ks).SignEnveloped(doc.Root())
	if err != nil {
		t.Fatal(err)
	}
	out := etree.NewDocument()
	out.SetRoot(signed)
	body, err := out.WriteToBytes()
	if err != nil {
		t.Fatal(err)
	}
	state, err := newSAML(&SAMLConfig{EntityID: "monitor", ACSURL: "http://127.0.0.1/api/v1/auth/saml/acs", SSOURL: "https://idp.example/sso", CertPEM: pemCert, Role: "viewer"})
	if err != nil {
		t.Fatal(err)
	}
	name, err := state.verify(body)
	if err != nil || name != "ada@example.com" {
		t.Fatalf("name %q err %v", name, err)
	}
	if _, err := state.verify([]byte(`<Assertion xmlns="urn:oasis:names:tc:SAML:2.0:assertion"><Subject><NameID>ada@example.com</NameID></Subject></Assertion>`)); err == nil {
		t.Fatal("accepted an unsigned assertion")
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	ts.Close()
	s := &Server{saml: state}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/saml/login", nil)
	rr := httptest.NewRecorder()
	s.handleSAMLLogin(rr, req)
	if rr.Code != http.StatusFound || !strings.Contains(rr.Header().Get("Location"), "SAMLRequest=") {
		t.Fatalf("login %d %s", rr.Code, rr.Header().Get("Location"))
	}
	form := url.Values{}
	form.Set("SAMLResponse", base64.StdEncoding.EncodeToString(body))
	req = httptest.NewRequest(http.MethodPost, "/api/v1/auth/saml/acs", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr = httptest.NewRecorder()
	s.handleSAMLACS(rr, req)
	loc := rr.Header().Get("Location")
	if rr.Code != http.StatusFound || !strings.Contains(loc, "token=") {
		t.Fatalf("acs %d %s", rr.Code, loc)
	}
	tok := strings.TrimPrefix(strings.TrimPrefix(loc, "/?token="), "/")
	if i := strings.Index(tok, "token="); i >= 0 {
		tok = tok[i+len("token="):]
	}
	if u, ok := state.session(tok); !ok || u.Name != "ada@example.com" || u.Role != RoleViewer {
		t.Fatalf("session %+v %v tok %q", u, ok, tok)
	}
	_ = cert
}
