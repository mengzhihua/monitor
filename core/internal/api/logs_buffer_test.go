package api

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"testing"

	"github.com/mengzhihua/monitor/core/internal/collect"
)

func TestLogsUsesCollectorFunctionAndForwardsFilters(t *testing.T) {
	argsSeen := make(chan map[string]string, 1)
	ts, _ := newTestServer(t, Options{ExtraFunctions: []collect.Function{{
		Name: "logs",
		Run: func(ctx context.Context, args map[string]string) (any, error) {
			if _, ok := ctx.Deadline(); !ok {
				t.Error("logs function has no deadline")
			}
			argsSeen <- args
			return collect.Table{Columns: []string{"message"}, Total: 1, Rows: []any{collect.LogRow{Message: "cached fixture"}}}, nil
		},
	}}})
	var response struct {
		Function string `json:"function"`
		Result   struct {
			Total int              `json:"total"`
			Rows  []collect.LogRow `json:"rows"`
		} `json:"result"`
	}
	r := getJSON(t, ts.URL+"/api/v1/logs?source=unified&query=fixture&after=10&before=20&limit=3&channel=System&unit=test&priority=err&boot=-1&cursor=abc&xpath=filter", &response)
	if r.StatusCode != http.StatusOK || response.Function != "logs" || response.Result.Total != 1 || len(response.Result.Rows) != 1 || response.Result.Rows[0].Message != "cached fixture" {
		t.Fatalf("status=%d response=%+v", r.StatusCode, response)
	}
	expected := map[string]string{"source": "unified", "query": "fixture", "after": "10", "before": "20", "limit": "3", "channel": "System", "unit": "test", "priority": "err", "boot": "-1", "cursor": "abc", "xpath": "filter"}
	if actual := <-argsSeen; !reflect.DeepEqual(actual, expected) {
		t.Fatalf("filters: %v", actual)
	}
}

func TestLogsFunctionFailureIsNotAnEmptySuccess(t *testing.T) {
	ts, _ := newTestServer(t, Options{ExtraFunctions: []collect.Function{{
		Name: "logs",
		Run: func(context.Context, map[string]string) (any, error) {
			return nil, errors.New("stream unavailable")
		},
	}}})
	if r := getJSON(t, ts.URL+"/api/v1/logs", nil); r.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status=%d", r.StatusCode)
	}
}
