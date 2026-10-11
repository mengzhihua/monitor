package api

import (
	"encoding/json"
	"testing"
)

func TestInfoNodeCountsRespectRoomScope(t *testing.T) {
	f := newRoomFixture(t)
	for _, token := range []string{"scoped-token", "admin-token", "wildcard-token"} {
		w := viewRequest(t, f.s, "GET", "/api/v1/info", token, "", 200)
		var info struct {
			Nodes int `json:"nodes_count"`
			ACLK  struct {
				Nodes int `json:"nodes"`
			} `json:"aclk"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &info); err != nil {
			t.Fatal(err)
		}
		want := 5
		if token == "scoped-token" {
			want = 3
		}
		if info.Nodes != want || info.ACLK.Nodes != want-1 {
			t.Fatalf("counts include hidden nodes for %s: %+v", token, info)
		}
	}
}
