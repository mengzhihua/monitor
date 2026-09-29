package api

import (
	"bytes"
	"compress/flate"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/beevik/etree"
	dsig "github.com/russellhaering/goxmldsig"
)

// SAMLConfig is web.saml. The ACS verifies an enveloped RSA signature against idp_cert.
type SAMLConfig struct {
	EntityID string `yaml:"entity_id"`
	ACSURL   string `yaml:"acs_url"`
	SSOURL   string `yaml:"sso_url"`
	CertPEM  string `yaml:"idp_cert"`
	Role     string `yaml:"role"`
}

type samlState struct {
	cfg  SAMLConfig
	cert *x509.Certificate
	mu   sync.Mutex
	sess map[string]oidcSession
}

func newSAML(cfg *SAMLConfig) (*samlState, error) {
	if cfg == nil || cfg.SSOURL == "" {
		return nil, nil
	}
	block, _ := pem.Decode([]byte(cfg.CertPEM))
	if block == nil {
		return nil, errors.New("web.saml: idp_cert is not PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("web.saml: %w", err)
	}
	role := cfg.Role
	if role == "" {
		role = string(RoleViewer)
	}
	c := *cfg
	c.Role = role
	return &samlState{cfg: c, cert: cert, sess: map[string]oidcSession{}}, nil
}

func (s *samlState) session(tok string) (User, bool) {
	if s == nil || tok == "" {
		return User{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.sess[tok]
	if !ok || !time.Now().Before(u.Expires) {
		delete(s.sess, tok)
		return User{}, false
	}
	return u.User, true
}

func (s *Server) handleSAMLLogin(w http.ResponseWriter, r *http.Request) {
	if s.saml == nil {
		http.Error(w, "saml not configured", http.StatusNotFound)
		return
	}
	id := make([]byte, 16)
	_, _ = rand.Read(id)
	reqID := fmt.Sprintf("_%x", id)
	body := fmt.Sprintf(`<samlp:AuthnRequest xmlns:samlp="urn:oasis:names:tc:SAML:2.0:protocol" xmlns:saml="urn:oasis:names:tc:SAML:2.0:assertion" ID="%s" Version="2.0" IssueInstant="%s" AssertionConsumerServiceURL="%s"><saml:Issuer>%s</saml:Issuer></samlp:AuthnRequest>`,
		xmlEscape(reqID), time.Now().UTC().Format(time.RFC3339), xmlEscape(s.saml.cfg.ACSURL), xmlEscape(s.saml.cfg.EntityID))
	var buf bytes.Buffer
	zw, _ := flate.NewWriter(&buf, flate.DefaultCompression)
	_, _ = zw.Write([]byte(body))
	_ = zw.Close()
	q := url.Values{}
	q.Set("SAMLRequest", base64.StdEncoding.EncodeToString(buf.Bytes()))
	http.Redirect(w, r, s.saml.cfg.SSOURL+"?"+q.Encode(), http.StatusFound)
}

func (s *Server) handleSAMLACS(w http.ResponseWriter, r *http.Request) {
	if s.saml == nil {
		http.Error(w, "saml not configured", http.StatusNotFound)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid saml form", http.StatusBadRequest)
		return
	}
	raw, err := base64.StdEncoding.DecodeString(r.FormValue("SAMLResponse"))
	if err != nil {
		http.Error(w, "invalid saml response", http.StatusBadRequest)
		return
	}
	name, err := s.saml.verify(raw)
	if err != nil || name == "" {
		http.Error(w, "invalid saml assertion", http.StatusUnauthorized)
		return
	}
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		http.Error(w, "entropy", http.StatusInternalServerError)
		return
	}
	tok := base64.RawURLEncoding.EncodeToString(b[:])
	user := User{Name: name, Role: Role(s.saml.cfg.Role), principal: viewPrincipal("saml", name)}
	s.saml.mu.Lock()
	s.saml.sess[tok] = oidcSession{User: user, Expires: time.Now().Add(8 * time.Hour)}
	s.saml.mu.Unlock()
	http.Redirect(w, r, "/?token="+url.QueryEscape(tok), http.StatusFound)
}

func (s *samlState) verify(body []byte) (string, error) {
	doc := etree.NewDocument()
	if err := doc.ReadFromBytes(body); err != nil {
		return "", err
	}
	target := doc.Root()
	if target == nil {
		return "", errors.New("empty")
	}
	if local(target.Tag) != "Assertion" {
		if found := findLocal(target, "Assertion"); found != nil {
			target = found
		}
	}
	ctx := dsig.NewDefaultValidationContext(&dsig.MemoryX509CertificateStore{Roots: []*x509.Certificate{s.cert}})
	validated, err := ctx.Validate(target)
	if err != nil {
		return "", err
	}
	name := textLocal(validated, "NameID")
	if name == "" {
		return "", errors.New("missing NameID")
	}
	if until := attrLocal(validated, "Conditions", "NotOnOrAfter"); until != "" {
		t, err := time.Parse(time.RFC3339, until)
		if err != nil || !time.Now().Before(t) {
			return "", errors.New("assertion expired")
		}
	}
	return name, nil
}

func findLocal(el *etree.Element, name string) *etree.Element {
	if el == nil {
		return nil
	}
	if local(el.Tag) == name {
		return el
	}
	for _, c := range el.ChildElements() {
		if f := findLocal(c, name); f != nil {
			return f
		}
	}
	return nil
}

func textLocal(el *etree.Element, name string) string {
	f := findLocal(el, name)
	if f == nil {
		return ""
	}
	return strings.TrimSpace(f.Text())
}

func attrLocal(el *etree.Element, element, attr string) string {
	f := findLocal(el, element)
	if f == nil {
		return ""
	}
	return f.SelectAttrValue(attr, "")
}

func local(tag string) string {
	if i := strings.LastIndex(tag, "}"); i >= 0 {
		return tag[i+1:]
	}
	return tag
}

func xmlEscape(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// readLimited is used by tests that post a raw body.
func readLimited(r io.Reader, n int64) ([]byte, error) {
	return io.ReadAll(io.LimitReader(r, n))
}
