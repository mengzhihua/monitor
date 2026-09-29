package trapper

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
	"github.com/mengzhihua/monitor/core/internal/tsdb"
)

func TestSenderFrameStoresValue(t *testing.T) {
	db, err := tsdb.Open(tsdb.Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	reg := registry.New(&registry.Host{ID: "h", Hostname: "h", UpdateEvery: 1}, db)
	payload, _ := json.Marshal(map[string]any{
		"request": "sender data",
		"psk":     "secret",
		"data":    []map[string]any{{"host": "web01", "key": "load", "value": "1.25", "clock": time.Now().Unix()}},
	})
	var buf bytes.Buffer
	buf.Write(header)
	var nbuf [8]byte
	binary.LittleEndian.PutUint64(nbuf[:], uint64(len(payload)))
	buf.Write(nbuf[:])
	buf.Write(payload)
	var out bytes.Buffer
	if err := Handle(struct {
		io.Reader
		io.Writer
	}{bytes.NewReader(buf.Bytes()), &out}, reg, "secret"); err != nil {
		t.Fatal(err)
	}
	ch, ok := reg.Chart("trapper.web01.load")
	if !ok {
		t.Fatal("missing chart")
	}
	_, vals := ch.LastValues()
	if vals["value"] != 1.25 {
		t.Fatalf("%+v", vals)
	}
	if !bytes.Contains(out.Bytes(), []byte(`"response":"success"`)) {
		t.Fatalf("%s", out.Bytes())
	}
	buf.Reset()
	payload, _ = json.Marshal(map[string]any{"request": "sender data", "data": []map[string]any{{"host": "web01", "key": "load", "value": "1"}}})
	buf.Write(header)
	binary.LittleEndian.PutUint64(nbuf[:], uint64(len(payload)))
	buf.Write(nbuf[:])
	buf.Write(payload)
	out.Reset()
	if err := Handle(struct {
		io.Reader
		io.Writer
	}{bytes.NewReader(buf.Bytes()), &out}, reg, "secret"); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out.Bytes(), []byte(`"response":"failed"`)) {
		t.Fatalf("psk %s", out.Bytes())
	}
}
