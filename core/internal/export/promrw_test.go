package export

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

func TestFlushPrometheusRemoteWrite(t *testing.T) {
	var got []byte
	var enc, ctype string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = io.ReadAll(r.Body)
		enc = r.Header.Get("Content-Encoding")
		ctype = r.Header.Get("Content-Type")
		w.WriteHeader(204)
	}))
	defer srv.Close()
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	reg.AddChart(&registry.Chart{ID: "system.ram", Dimensions: []*registry.Dimension{{ID: "used"}}})
	_ = reg.Collect("system.ram", time.Unix(1_700_000_000, 0), map[string]float64{"used": 7})
	e := New(reg, []Destination{{Type: "prometheus", URL: srv.URL, Prefix: "monitor"}}, nil)
	if err := e.flushPromRW(context.Background(), e.dest[0]); err != nil {
		t.Fatal(err)
	}
	if enc != "snappy" || ctype != "application/x-protobuf" {
		t.Fatalf("headers encoding=%s type=%s", enc, ctype)
	}
	n, rest, err := snappyDecodedLen(got)
	if err != nil || n <= 0 || len(rest) == 0 {
		t.Fatalf("snappy %v n=%d rest=%d", err, n, len(rest))
	}
}

func TestSnappyRoundtripLen(t *testing.T) {
	raw := []byte("hello monitor prometheus remote write")
	enc := snappyEncode(raw)
	n, _, err := snappyDecodedLen(enc)
	if err != nil || n != len(raw) {
		t.Fatalf("n=%d err=%v", n, err)
	}
}
