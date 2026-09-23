package collect

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net/http"
	"net/http/httptest"
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

func TestIPPPrinters(t *testing.T) {
	var body bytes.Buffer
	body.Write([]byte{0x02, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01})
	body.WriteByte(0x04)
	writeIPPString(&body, 0x42, "printer-name", "HP")
	writeIPPEnum(&body, "printer-state", 4)
	body.WriteByte(0x03)
	dests := parseIPPPrinters(body.Bytes())
	if len(dests) != 1 || dests[0].Name != "HP" || dests[0].State != "printing" {
		t.Fatalf("%+v", dests)
	}
	var jobs bytes.Buffer
	jobs.Write([]byte{0x02, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01, 0x02, 0x03})
	if parseIPPJobCount(jobs.Bytes()) != 1 {
		t.Fatal("job count")
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req, _ := io.ReadAll(r.Body)
		op := uint16(0)
		if len(req) >= 4 {
			op = binary.BigEndian.Uint16(req[2:4])
		}
		if op == 0x000a {
			_, _ = w.Write(jobs.Bytes())
			return
		}
		_, _ = w.Write(body.Bytes())
	}))
	defer srv.Close()
	addr := srv.Listener.Addr().String()
	got, n, err := readIPP(addr, time.Second)
	if err != nil || len(got) != 1 || got[0].Name != "HP" || n != 1 {
		t.Fatalf("%v %d %v", got, n, err)
	}
}
