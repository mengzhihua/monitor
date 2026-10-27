package collect

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

func TestSQLQueryFailureMarksUnsupported(t *testing.T) {
	phase := "ok"
	s := &sqlCollector{
		cfg: sqlConfig{Driver: "mysql", Query: "SELECT 1 AS value", Command: "mysql", Timeout: time.Second},
		run: func(context.Context, string, ...string) ([]byte, error) {
			switch phase {
			case "fail":
				return nil, errors.New("mysql: access denied for user")
			case "empty":
				return []byte("\n"), nil
			default:
				return []byte("value 42\n"), nil
			}
		},
	}
	reg := registry.New(&registry.Host{Hostname: "t", UpdateEvery: 1}, nil)
	if err := s.Init(reg); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0)
	if err := s.Collect(context.Background(), reg, now); err != nil {
		t.Fatal(err)
	}
	query := mustChart(t, reg, "sql.mysql_query")
	timing := mustChart(t, reg, "sql.mysql_query_time")
	if _, vals := query.LastValues(); vals["value"] != 42 {
		t.Fatalf("query %+v", vals)
	}

	phase = "fail"
	if err := s.Collect(context.Background(), reg, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if query.Unsupported == "" || timing.Unsupported == "" {
		t.Fatalf("query %q timing %q", query.Unsupported, timing.Unsupported)
	}
	if query.LastCollectionTime() != now.Unix() {
		t.Fatalf("failure wrote a sample at %d", query.LastCollectionTime())
	}

	phase = "ok"
	if err := s.Collect(context.Background(), reg, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if query.Unsupported != "" || timing.Unsupported != "" {
		t.Fatalf("stuck query %q timing %q", query.Unsupported, timing.Unsupported)
	}
	if _, vals := query.LastValues(); vals["value"] != 42 {
		t.Fatalf("recovered %+v", vals)
	}

	phase = "empty"
	if err := s.Collect(context.Background(), reg, now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	if query.Unsupported == "" || timing.Unsupported == "" {
		t.Fatal("empty result stayed supported")
	}
	if query.LastCollectionTime() != now.Add(2*time.Second).Unix() {
		t.Fatalf("empty result wrote a sample at %d", query.LastCollectionTime())
	}
}
