package collect

import (
	"context"
	"io"
	"net/http"
	"strings"
	"time"
)

func httpText(ctx context.Context, client *http.Client, method, rawURL string, headers map[string]string, body string, limit int64) (code int, text string, elapsed float64, err error) {
	if client == nil {
		client = http.DefaultClient
	}
	if method == "" {
		method = http.MethodGet
	}
	if limit <= 0 {
		limit = 1 << 20
	}
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, rdr)
	if err != nil {
		return 0, "", 0, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	start := time.Now()
	resp, err := client.Do(req)
	elapsed = time.Since(start).Seconds()
	if err != nil {
		return 0, "", elapsed, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return resp.StatusCode, "", elapsed, err
	}
	if int64(len(b)) > limit {
		b = b[:limit]
	}
	return resp.StatusCode, string(b), elapsed, nil
}
