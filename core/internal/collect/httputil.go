package collect

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/ingest"
	"github.com/mengzhihua/monitor/core/internal/registry"
)

// CollectorTLS verifies HTTPS by default. Private CAs are explicitly configured.
type CollectorTLS struct {
	CAFile             string `yaml:"ca_file"`
	InsecureSkipVerify bool   `yaml:"insecure_skip_verify"`
}

func collectorHTTPClient(timeout time.Duration, cfg CollectorTLS) (*http.Client, error) {
	tc := &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: cfg.InsecureSkipVerify} // operator opt-in
	if cfg.CAFile != "" {
		pem, err := os.ReadFile(cfg.CAFile)
		if err != nil {
			return nil, fmt.Errorf("TLS CA: %w", err)
		}
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("TLS CA: no certificates in %s", cfg.CAFile)
		}
		tc.RootCAs = pool
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = tc
	return &http.Client{Timeout: timeout, Transport: tr}, nil
}

func httpGet(ctx context.Context, client *http.Client, url string) ([]byte, error) {
	return httpGetAuth(ctx, client, url, "", "")
}

func httpPost(ctx context.Context, client *http.Client, url string) ([]byte, error) {
	return httpDo(ctx, client, http.MethodPost, url, "", "", nil, "", "")
}

func httpPostBody(ctx context.Context, client *http.Client, url, contentType, body string, hdr map[string]string) ([]byte, error) {
	return httpDo(ctx, client, http.MethodPost, url, contentType, body, hdr, "", "")
}

func httpDo(ctx context.Context, client *http.Client, method, url, contentType, body string, hdr map[string]string, user, pass string) ([]byte, error) {
	var rdr io.Reader
	if body != "" {
		rdr = bytes.NewReader([]byte(body))
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rdr)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for k, v := range hdr {
		if v != "" {
			req.Header.Set(k, v)
		}
	}
	if user != "" {
		req.SetBasicAuth(user, pass)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return out, fmt.Errorf("%s: %s", url, resp.Status)
	}
	return out, nil
}

func httpGetToken(ctx context.Context, client *http.Client, url, token string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return body, fmt.Errorf("%s: %s", url, resp.Status)
	}
	return body, nil
}

func httpGetTokenTry(ctx context.Context, client *http.Client, urls []string, token string) (string, []byte, error) {
	var last error
	for _, u := range urls {
		if u == "" {
			continue
		}
		b, err := httpGetToken(ctx, client, u, token)
		if err != nil {
			last = err
			continue
		}
		return u, b, nil
	}
	if last == nil {
		last = fmt.Errorf("no urls")
	}
	return "", nil, last
}

func ensureDim(reg *registry.Registry, chartID, dimID string, d *registry.Dimension) {
	c, ok := reg.Chart(chartID)
	if !ok {
		return
	}
	if d == nil {
		d = &registry.Dimension{ID: dimID}
	}
	if d.ID == "" {
		d.ID = dimID
	}
	c.AddDimension(d)
}

func readBody(resp *http.Response) ([]byte, error) {
	return io.ReadAll(io.LimitReader(resp.Body, 8<<20))
}

func httpGetAuth(ctx context.Context, client *http.Client, url, user, pass string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if user != "" {
		req.SetBasicAuth(user, pass)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return body, fmt.Errorf("%s: %s", url, resp.Status)
	}
	return body, nil
}

func httpGetTry(ctx context.Context, client *http.Client, urls []string) (string, []byte, error) {
	var last error
	for _, u := range urls {
		if u == "" {
			continue
		}
		b, err := httpGet(ctx, client, u)
		if err != nil {
			last = err
			continue
		}
		return u, b, nil
	}
	if last == nil {
		last = fmt.Errorf("no urls")
	}
	return "", nil, last
}

func promSamples(body string) []ingest.Sample {
	return ingest.ParseOpenMetrics(body)
}

func promSum(samples []ingest.Sample, name string) float64 {
	var n float64
	for _, s := range samples {
		if s.Name == name {
			n += s.Value
		}
	}
	return n
}

func promSumPrefixed(samples []ingest.Sample, names ...string) float64 {
	var n float64
	for _, s := range samples {
		for _, name := range names {
			if s.Name == name {
				n += s.Value
				break
			}
		}
	}
	return n
}

func promLabel(s ingest.Sample, key string) string {
	for _, l := range s.Labels {
		if l.Key == key {
			return l.Value
		}
	}
	return ""
}

func promSumByLabel(samples []ingest.Sample, name, label string) map[string]float64 {
	out := map[string]float64{}
	for _, s := range samples {
		if s.Name != name {
			continue
		}
		out[promLabel(s, label)] += s.Value
	}
	return out
}

func httpStatusClass(code string) string {
	if len(code) >= 1 {
		switch code[0] {
		case '1':
			return "1xx"
		case '2':
			return "2xx"
		case '3':
			return "3xx"
		case '4':
			return "4xx"
		case '5':
			return "5xx"
		}
	}
	return "other"
}

func parseKVTab(s string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "\t")
		if !ok {
			k, v, ok = strings.Cut(line, " ")
		}
		if !ok {
			k, v, ok = strings.Cut(line, "=")
		}
		if !ok {
			continue
		}
		out[strings.TrimSpace(k)] = strings.TrimSpace(strings.Trim(v, `"`))
	}
	return out
}
