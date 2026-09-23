package api

import (
	"encoding/csv"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/mengzhihua/monitor/core/internal/operations"
	"github.com/mengzhihua/monitor/core/internal/registry"
)

func TestHandlingHistoryAuthorizationExportsAndStaleness(t *testing.T) {
	s, err := New(registry.New(&registry.Host{}, nil), nil, nil, Options{Token: "admin-token", Users: []User{{Name: "reader", Token: "reader-token", Role: RoleViewer}}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.operations.Update("old-episode", "operator", "comment", " =HYPERLINK(\"example\")\ncomma, quote\"", 0, operations.Target{Hostname: "=host", Node: "n1", Name: "ram", Severity: "WARNING"})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	get := func(path, token string, want int) []byte {
		t.Helper()
		req, _ := http.NewRequest("GET", ts.URL+path, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != want {
			t.Fatalf("status=%d want=%d: %s", resp.StatusCode, want, b)
		}
		if want == 200 && resp.Header.Get("Cache-Control") != "no-store" {
			t.Fatal("sensitive history cacheable")
		}
		return b
	}
	const base = "/api/v1/operations/history"
	get(base, "", 401)
	get(base+"/export?format=json", "", 401)
	var page operations.HistoryPage
	if err := json.Unmarshal(get(base+"?node=n1&actor=operator", "reader-token", 200), &page); err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || page.Records[0].Problem.Hostname != "=host" {
		t.Fatal(page)
	}
	for _, query := range []string{"limit=101", "limit=0", "from=bad", "from=5&until=1", "status=resolved", "cursor=bad"} {
		get(base+"?"+query, "reader-token", 400)
	}
	get(base+"/export?format=json", "reader-token", 400)
	export := base + "/export?format="
	var full operations.HistoryPage
	if err := json.Unmarshal(get(export+"json&snapshot="+page.Snapshot, "reader-token", 200), &full); err != nil {
		t.Fatal(err)
	}
	if len(full.Records) != 1 || full.Records[0].History[0].Note != "=HYPERLINK(\"example\")\ncomma, quote\"" {
		t.Fatal(full)
	}
	b := get(export+"csv&snapshot="+page.Snapshot, "reader-token", 200)
	rows, err := csv.NewReader(strings.NewReader(strings.TrimPrefix(string(b), "\ufeff"))).ReadAll()
	if err != nil || len(rows) != 2 || rows[1][2] != "'=host" || rows[1][16] != "'=HYPERLINK(\"example\")\ncomma, quote\"" {
		t.Fatal(rows, err)
	}
	if _, err := s.operations.Update("old-episode", "operator", "comment", "changed", 1, operations.Target{}); err != nil {
		t.Fatal(err)
	}
	get(export+"json&snapshot="+url.QueryEscape(page.Snapshot), "reader-token", 409)
}

func TestHistoryCSVTextProtection(t *testing.T) {
	for _, v := range []string{"=1+1", "  +SUM(A1)", "\ttext", "\rtext", "\ntext", "\ufeff@command", "-1+1"} {
		if got := csvText(v); got != "'"+v {
			t.Fatalf("unsafe CSV %q", got)
		}
	}
	for _, v := range []string{"normal", "中文备注", "a,b\nnext", "123", ""} {
		if csvText(v) != v {
			t.Fatal("unnecessary alteration", v)
		}
	}
}
