package collect

import (
	"context"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

func TestParseLpstat(t *testing.T) {
	dests := parseLpstatPrinters("printer HP is idle.  enabled since Mon 01 Jan\nprinter PDF is printing.  enabled since Mon\nprinter BAD is stopped.  disabled since Mon\n")
	if len(dests) != 3 || dests[0].State != "idle" || dests[1].State != "printing" || dests[2].State != "stopped" {
		t.Fatalf("%+v", dests)
	}
	if parseLpstatJobs("HP-1 user 1024 bytes\nPDF-2 user 10 bytes\n") != 2 {
		t.Fatal("jobs")
	}
}

func TestCupsCollectorFixture(t *testing.T) {
	c := &cupsCollector{}
	c.run = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "-o" {
			return []byte("HP-1 root 1024\n"), nil
		}
		return []byte("printer HP is idle.  enabled since Mon\n"), nil
	}
	reg := registry.New(&registry.Host{Hostname: "t", UpdateEvery: 1}, nil)
	if err := c.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := c.Collect(context.Background(), reg, time.Unix(1_700_000_000, 0)); err != nil {
		t.Fatal(err)
	}
	ch, ok := reg.Chart("cups.dests")
	if !ok {
		t.Fatal("missing dests")
	}
	_, vals := ch.LastValues()
	if vals["idle"] != 1 {
		t.Fatalf("%v", vals)
	}
	if ch, ok := reg.Chart("cups.dest_state.HP"); !ok {
		t.Fatal("missing dest state")
	} else if ch.Context != "cups.dest_state" {
		t.Fatalf("context %q", ch.Context)
	}
}
