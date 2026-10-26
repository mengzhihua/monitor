package stream

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
)

// CertFiles are PEM paths for the agent client or the hub listener.
// Empty paths leave that part unconfigured.
type CertFiles struct {
	CertFile           string
	KeyFile            string
	CAFile             string
	RequireClientCert  bool
	InsecureSkipVerify bool
}

// ClientTLS builds the agent dialer config. A nil config means the caller
// should use the default system roots.
func ClientTLS(files CertFiles) (*tls.Config, error) {
	if files.CertFile == "" && files.KeyFile == "" && files.CAFile == "" && !files.InsecureSkipVerify {
		return nil, nil
	}
	cfg := &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: files.InsecureSkipVerify} //nolint:gosec // operator opt-in
	if (files.CertFile == "") != (files.KeyFile == "") {
		return nil, errors.New("stream cert_file and key_file are both required")
	}
	if files.CertFile != "" {
		cert, err := tls.LoadX509KeyPair(files.CertFile, files.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("stream client certificate: %w", err)
		}
		cfg.Certificates = []tls.Certificate{cert}
	}
	if files.CAFile != "" {
		pool, err := loadCertPool(files.CAFile)
		if err != nil {
			return nil, err
		}
		cfg.RootCAs = pool
	}
	return cfg, nil
}

// ServerTLS builds the HTTP listener config. A nil config means plain HTTP.
func ServerTLS(files CertFiles) (*tls.Config, error) {
	if files.CertFile == "" && files.KeyFile == "" && files.CAFile == "" && !files.RequireClientCert {
		return nil, nil
	}
	if files.CertFile == "" || files.KeyFile == "" {
		return nil, errors.New("web.tls_cert and web.tls_key are required to enable TLS")
	}
	cert, err := tls.LoadX509KeyPair(files.CertFile, files.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("server certificate: %w", err)
	}
	cfg := &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}}
	if files.CAFile != "" {
		pool, err := loadCertPool(files.CAFile)
		if err != nil {
			return nil, err
		}
		cfg.ClientCAs = pool
		cfg.ClientAuth = tls.VerifyClientCertIfGiven
	}
	if files.RequireClientCert {
		if cfg.ClientCAs == nil {
			return nil, errors.New("hub.require_client_cert requires web.client_ca")
		}
		cfg.ClientAuth = tls.RequireAndVerifyClientCert
	}
	return cfg, nil
}

func loadCertPool(path string) (*x509.CertPool, error) {
	pemBytes, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pemBytes) {
		return nil, fmt.Errorf("%s: no certificates", path)
	}
	return pool, nil
}
