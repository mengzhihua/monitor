package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/mengzhihua/monitor/core/internal/hub"
)

func TestHubConfigRevisionAndInitialConflict(t *testing.T) {
	hs, _, org, _ := newHubCfg(t, Options{})
	url := hs.URL + "/api/v1/hub/config?node=box-1"
	resp, initial, _ := putHubConfig(t, url, map[string]any{"yaml": "mode: agent\n", "if_updated": 0})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("first create status=%d", resp.StatusCode)
	}
	resp, _, _ = putHubConfig(t, url, map[string]any{"yaml": "mode: hub\n", "if_updated": 0})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("stale initial create status=%d", resp.StatusCode)
	}
	if err := org.SetApply("box-1", hub.ApplyState{Rev: initial.Updated, State: "applied"}); err != nil {
		t.Fatal(err)
	}
	resp, next, _ := putHubConfig(t, url, map[string]any{"yaml": "mode: agent\nglobal:\n  hostname: next\n", "if_updated": initial.Updated})
	if resp.StatusCode != http.StatusOK || next.Updated <= initial.Updated || !next.Pending {
		t.Fatalf("new desired config must have a new pending revision: status=%d initial=%d got=%+v", resp.StatusCode, initial.Updated, next)
	}
	resp, _, _ = putHubConfig(t, url, map[string]any{"yaml": "mode: agent\n", "if_updated": initial.Updated})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("reused revision status=%d", resp.StatusCode)
	}
	resp, got := getHubConfig(t, url, "")
	if resp.Header.Get("Cache-Control") != "no-store" || got.YAML != next.YAML {
		t.Fatalf("config response/cache changed: status=%d cache=%q yaml=%q", resp.StatusCode, resp.Header.Get("Cache-Control"), got.YAML)
	}
}

func TestHubConfigConcurrentCAS(t *testing.T) {
	hs, _, org, _ := newHubCfg(t, Options{})
	url := hs.URL + "/api/v1/hub/config?node=box-1"
	initial, err := org.SetConfig(hub.NodeConfig{NodeID: "box-1", YAML: "mode: agent\n"})
	if err != nil {
		t.Fatal(err)
	}
	const writers = 12
	start := make(chan struct{})
	statuses := make(chan int, writers)
	errors := make(chan error, writers)
	var wg sync.WaitGroup
	for i := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			payload, _ := json.Marshal(map[string]any{"disabled": []string{string(rune('a' + i))}, "if_updated": initial.Updated})
			req, _ := http.NewRequest(http.MethodPut, url, bytes.NewReader(payload))
			<-start
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				errors <- err
				return
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			statuses <- resp.StatusCode
		}()
	}
	close(start)
	wg.Wait()
	close(statuses)
	close(errors)
	for err := range errors {
		t.Error(err)
	}
	succeeded, conflicts := 0, 0
	for status := range statuses {
		switch status {
		case http.StatusOK:
			succeeded++
		case http.StatusConflict:
			conflicts++
		default:
			t.Errorf("unexpected status=%d", status)
		}
	}
	if succeeded != 1 || conflicts != writers-1 {
		t.Fatalf("succeeded=%d conflicts=%d", succeeded, conflicts)
	}
	if got, _ := org.GetConfig("box-1"); got.YAML != initial.YAML {
		t.Fatal("disabled-only update lost YAML")
	}
}

func TestHubConfigRejectsTrailingJSON(t *testing.T) {
	hs, _, org, _ := newHubCfg(t, Options{})
	req, _ := http.NewRequest(http.MethodPut, hs.URL+"/api/v1/hub/config?node=box-1", strings.NewReader(`{"yaml":"mode: agent\n"} {"yaml":"mode: hub\n"}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("multiple JSON values status=%d", resp.StatusCode)
	}
	if _, ok := org.GetConfig("box-1"); ok {
		t.Fatal("invalid request changed config")
	}
}
