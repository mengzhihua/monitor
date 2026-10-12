package api

import (
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/beevik/etree"
	dsig "github.com/russellhaering/goxmldsig"
)

func signAssertion(t *testing.T, ks dsig.X509KeyStore, id, name string) []byte {
	t.Helper()
	doc := etree.NewDocument()
	xml := fmt.Sprintf(`<Assertion xmlns="urn:oasis:names:tc:SAML:2.0:assertion" ID="%s"><Issuer>idp</Issuer><Subject><NameID>%s</NameID></Subject></Assertion>`, id, name)
	if err := doc.ReadFromString(xml); err != nil {
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
	return body
}

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
	body := signAssertion(t, ks, "_a1", "ada@example.com")
	body2 := signAssertion(t, ks, "_a2", "ada@example.com")
	state, err := newSAML(&SAMLConfig{EntityID: "monitor", ACSURL: "http://127.0.0.1/api/v1/auth/saml/acs", SSOURL: "https://idp.example/sso", CertPEM: pemCert, Role: "viewer"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := state.verify([]byte(`<Assertion xmlns="urn:oasis:names:tc:SAML:2.0:assertion" ID="_unsigned"><Subject><NameID>ada@example.com</NameID></Subject></Assertion>`)); err == nil {
		t.Fatal("accepted an unsigned assertion")
	}
	s := &Server{saml: state}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/saml/login", nil)
	rr := httptest.NewRecorder()
	s.handleSAMLLogin(rr, req)
	if rr.Code != http.StatusFound || !strings.Contains(rr.Header().Get("Location"), "SAMLRequest=") {
		t.Fatalf("login %d %s", rr.Code, rr.Header().Get("Location"))
	}
	post := func(raw []byte) *httptest.ResponseRecorder {
		form := url.Values{}
		form.Set("SAMLResponse", base64.StdEncoding.EncodeToString(raw))
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/saml/acs", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rr := httptest.NewRecorder()
		s.handleSAMLACS(rr, req)
		return rr
	}
	rr = post(body)
	loc := rr.Header().Get("Location")
	if rr.Code != http.StatusFound || !strings.Contains(loc, "token=") {
		t.Fatalf("acs %d %s", rr.Code, loc)
	}
	tok := loc[strings.Index(loc, "token=")+len("token="):]
	if u, ok := state.session(tok); !ok || u.Name != "ada@example.com" || u.Role != RoleViewer {
		t.Fatalf("session %+v %v tok %q", u, ok, tok)
	}
	if rr := post(body); rr.Code != http.StatusUnauthorized {
		t.Fatalf("replay status %d", rr.Code)
	}
	if rr := post(body2); rr.Code != http.StatusFound {
		t.Fatalf("second assertion %d", rr.Code)
	}
}
