package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type manageConfigJSON struct {
	Path    string `json:"path"`
	YAML    string `json:"yaml"`
	Updated int64  `json:"updated"`
	Size    int    `json:"size"`
	Backup  string `json:"backup"`
}

func TestManageConfigGetPut(t *testing.T) {
	path := filepath.Join(t.TempDir(), "monitor.yaml")
	initial := "mode: agent\nglobal:\n  hostname: box\n"
	if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}
	ts, _ := newTestServer(t, Options{ConfigPath: path})

	// GET returns the file text, its size and its mtime.
	resp, err := http.Get(ts.URL + "/api/v1/manage/config")
	if err != nil {
		t.Fatal(err)
	}
	var got manageConfigJSON
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Path != path || got.YAML != initial || got.Updated != info.ModTime().Unix() || got.Size != len(initial) {
		t.Fatalf("get = %+v", got)
	}

	put := func(body map[string]any) *http.Response {
		t.Helper()
		b, _ := json.Marshal(body)
		req, _ := http.NewRequest(http.MethodPut, ts.URL+"/api/v1/manage/config", bytes.NewReader(b))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}

	// invalid yaml -> 400 and the file is untouched
	if resp := put(map[string]any{"yaml": "mode: nonsense\n"}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid yaml status = %d", resp.StatusCode)
	}
	if cur, _ := os.ReadFile(path); string(cur) != initial {
		t.Fatalf("file changed after rejected put: %q", cur)
	}

	// valid put -> 200, .bak keeps the previous content, file replaced
	next := "mode: agent\nglobal:\n  hostname: renamed\n  update_every: 2\n"
	resp = put(map[string]any{"yaml": next})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("put status = %d", resp.StatusCode)
	}
	if bak, _ := os.ReadFile(path + ".bak"); string(bak) != initial {
		t.Fatalf("backup = %q", bak)
	}
	if cur, _ := os.ReadFile(path); string(cur) != next {
		t.Fatalf("file = %q", cur)
	}
	st, _ := os.Stat(path)
	if st.ModTime().Unix() == 0 {
		t.Fatal("no mtime after put")
	}

	// stale if_updated -> 409 without touching the file
	if resp := put(map[string]any{"yaml": next, "if_updated": st.ModTime().Unix() + 9999}); resp.StatusCode != http.StatusConflict {
		t.Fatalf("conflict status = %d", resp.StatusCode)
	}
	if cur, _ := os.ReadFile(path); string(cur) != next {
		t.Fatal("file changed on conflict")
	}
	// a matching if_updated is accepted
	if resp := put(map[string]any{"yaml": next, "if_updated": st.ModTime().Unix()}); resp.StatusCode != http.StatusOK {
		t.Fatalf("matching if_updated status = %d", resp.StatusCode)
	}
}

// GET on a missing file still answers, with zero mtime and empty text.
func TestManageConfigMissingFile(t *testing.T) {
	ts, _ := newTestServer(t, Options{ConfigPath: filepath.Join(t.TempDir(), "monitor.yaml")})
	resp, err := http.Get(ts.URL + "/api/v1/manage/config")
	if err != nil {
		t.Fatal(err)
	}
	var got manageConfigJSON
	_ = json.NewDecoder(resp.Body).Decode(&got)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || got.YAML != "" || got.Updated != 0 || got.Size != 0 {
		t.Fatalf("get = %d %+v", resp.StatusCode, got)
	}
}

func TestManageRestart(t *testing.T) {
	var calls int32
	ts, _ := newTestServer(t, Options{Restart: func() { atomic.AddInt32(&calls, 1) }})
	resp, err := http.Post(ts.URL+"/api/v1/manage/restart", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		OK bool `json:"ok"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !out.OK {
		t.Fatalf("restart = %d %+v", resp.StatusCode, out)
	}
	// the callback fires asynchronously, after the response was flushed
	deadline := time.Now().Add(5 * time.Second)
	for atomic.LoadInt32(&calls) == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("restart callback calls = %d", calls)
	}
}

func TestManageRestartUnsupported(t *testing.T) {
	ts, _ := newTestServer(t, Options{})
	resp, err := http.Post(ts.URL+"/api/v1/manage/restart", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

// The config file carries credentials: every manage/config route and the
// restart route refuse non-admin tokens, including reads.
func TestManageConfigAdminOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "monitor.yaml")
	if err := os.WriteFile(path, []byte("mode: agent\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ts, _ := newTestServer(t, Options{ConfigPath: path, Users: []User{
		{Name: "a", Token: "tok-admin", Role: RoleAdmin},
		{Name: "v", Token: "tok-view", Role: RoleViewer},
	}})
	do := func(method, target, body string) int {
		t.Helper()
		var rdr io.Reader
		if body != "" {
			rdr = strings.NewReader(body)
		}
		req, _ := http.NewRequest(method, ts.URL+target, rdr)
		req.Header.Set("Authorization", "Bearer tok-view")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if got := do(http.MethodGet, "/api/v1/manage/config", ""); got != http.StatusForbidden {
		t.Errorf("viewer GET manage/config = %d", got)
	}
	if got := do(http.MethodPut, "/api/v1/manage/config", `{"yaml":"mode: agent"}`); got != http.StatusForbidden {
		t.Errorf("viewer PUT manage/config = %d", got)
	}
	if got := do(http.MethodPost, "/api/v1/manage/restart", ""); got != http.StatusForbidden {
		t.Errorf("viewer POST manage/restart = %d", got)
	}
	if cur, _ := os.ReadFile(path); string(cur) != "mode: agent\n" {
		t.Fatalf("file = %q", cur)
	}
}
