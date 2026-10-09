package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestAIPreviewConcurrencyGateRejectsBeforeSnapshot(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		owners []string
	}{
		{"same_identity", []string{viewPrincipal("legacy", "ai-admin-token")}},
		{"global_capacity", []string{viewPrincipal("static", "other-one"), viewPrincipal("static", "other-two")}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			s, _ := aiTestServer(t, Options{Token: "ai-admin-token"})
			s.analysis.mu.Lock()
			for _, owner := range testCase.owners {
				s.analysis.preparing[owner] = true
			}
			s.analysis.mu.Unlock()
			w := viewRequest(t, s, "POST", aiPreviewPath, "ai-admin-token", aiPreviewBody, http.StatusTooManyRequests)
			if w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("preview limit response is cacheable")
			}
			s.analysis.mu.Lock()
			previewCount, preparingCount := len(s.analysis.previews), len(s.analysis.preparing)
			for _, owner := range testCase.owners {
				delete(s.analysis.preparing, owner)
			}
			s.analysis.mu.Unlock()
			if previewCount != 0 || preparingCount != len(testCase.owners) {
				t.Fatalf("rejected preview produced state: previews=%d preparing=%d", previewCount, preparingCount)
			}
			// Once the occupied slots are released, this same identity can
			// generate a snapshot and the handler must release its own slot.
			aiReadPreview(t, s, "ai-admin-token", aiPreviewBody)
			s.analysis.mu.Lock()
			previewCount, preparingCount = len(s.analysis.previews), len(s.analysis.preparing)
			s.analysis.mu.Unlock()
			if previewCount != 1 || preparingCount != 0 {
				t.Fatalf("successful preview retained a slot: previews=%d preparing=%d", previewCount, preparingCount)
			}
		})
	}
}

func TestAIPreviewCanceledRequestReleasesSlot(t *testing.T) {
	s, _ := aiTestServer(t, Options{Token: "ai-admin-token"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := httptest.NewRequest(http.MethodPost, aiPreviewPath, strings.NewReader(aiPreviewBody)).WithContext(ctx)
	r.Header.Set("Authorization", "Bearer ai-admin-token")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	s.analysis.mu.Lock()
	previewCount, preparingCount := len(s.analysis.previews), len(s.analysis.preparing)
	s.analysis.mu.Unlock()
	if previewCount != 0 || preparingCount != 0 {
		t.Fatalf("canceled request retained state: previews=%d preparing=%d", previewCount, preparingCount)
	}
	aiReadPreview(t, s, "ai-admin-token", aiPreviewBody)
	s.analysis.mu.Lock()
	preparingCount = len(s.analysis.preparing)
	s.analysis.mu.Unlock()
	if preparingCount != 0 {
		t.Fatal("new request after cancellation retained its slot")
	}
}

func TestAIProviderRateLimitIsSafeAndNotRetried(t *testing.T) {
	const providerSecret = "provider-secret-never-in-errors"
	const privateBody = "private-provider-response-never-in-errors"
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer "+providerSecret {
			t.Error("provider did not receive configured fixture credential")
		}
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, privateBody+" "+providerSecret)
	}))
	defer provider.Close()
	s, _ := aiTestServer(t, Options{Token: "ai-admin-token"})
	viewRequest(t, s, "PUT", aiSettingsPath, "ai-admin-token", aiJSON(t, map[string]any{
		"base_url": provider.URL + "/v1", "model": "fixture-model", "api_key": providerSecret, "allow_private": true, "revision": 0,
	}), http.StatusOK)
	p := aiReadPreview(t, s, "ai-admin-token", aiPreviewBody)
	body := aiJSON(t, map[string]any{"preview_id": p.ID, "revision": 1, "question": "Explain the selected metrics"})
	w := viewRequest(t, s, "POST", aiAnalyzePath, "ai-admin-token", body, http.StatusTooManyRequests)
	if w.Header().Get("Cache-Control") != "no-store" || !strings.Contains(w.Body.String(), "rate limit") {
		t.Fatal("provider rate limit response lacks safe status text or cache control")
	}
	for _, secret := range []string{providerSecret, privateBody, provider.URL} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatal("provider failure disclosed private request or response data")
		}
	}
	viewRequest(t, s, "POST", aiAnalyzePath, "ai-admin-token", body, http.StatusConflict)
	if calls.Load() != 1 {
		t.Fatalf("rate-limited preview triggered %d provider calls", calls.Load())
	}
}

func TestAIPreviewClampsOpenBucketTimestamp(t *testing.T) {
	s, _ := aiTestServer(t, Options{Token: "ai-admin-token"})
	// A fresh sample lies in the open final bucket of a 24-hour snapshot.
	// Its nominal 720-second bucket end is usually ahead of the snapshot;
	// the reported timestamp must never imply future observations.
	if err := s.reg.Collect("test.analysis", time.Now(), map[string]float64{"value": 42}); err != nil {
		t.Fatal(err)
	}
	p := aiReadPreview(t, s, "ai-admin-token", `{"node":"","charts":["test.analysis"],"window":86400}`)
	found := false
	for _, chart := range p.Snapshot.Charts {
		for _, dimension := range chart.Dimensions {
			if dimension.LastAt != nil && *dimension.LastAt > p.Snapshot.Before {
				t.Fatalf("dimension %s reports future data: last_at=%d before=%d", dimension.ID, *dimension.LastAt, p.Snapshot.Before)
			}
			if dimension.ID == "value" {
				found = true
				if dimension.LastAt == nil || dimension.Count == 0 {
					t.Fatal("fresh metric disappeared from snapshot")
				}
			}
		}
	}
	if !found {
		t.Fatal("selected metric missing from snapshot")
	}
}
