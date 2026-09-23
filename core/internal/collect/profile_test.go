package collect

import (
	"context"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

func TestProfileCollector(t *testing.T) {
	p := &profileCollector{}
	reg := registry.New(&registry.Host{Hostname: "t", UpdateEvery: 1}, nil)
	if err := p.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := p.Collect(context.Background(), reg, time.Unix(1_700_000_000, 0)); err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Chart("profile.goroutines"); !ok {
		t.Fatal("missing profile.goroutines")
	}
	tab, err := p.Functions()[0].Run(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	rows := tab.(Table).Rows
	if len(rows) == 0 {
		t.Fatal("empty profile")
	}
}
