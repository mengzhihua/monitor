package collect

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"net"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// mongodbConfig is collectors.modules.mongodb (Netdata go.d mongodb).
type mongodbConfig struct {
	Address  string        `yaml:"address"` // host:port (default 127.0.0.1:27017)
	Timeout  time.Duration `yaml:"timeout"`
	Database string        `yaml:"database"` // command db, default admin
}

type mongodbCollector struct {
	cfg mongodbConfig
}

func init() {
	Register("mongodb", func() Collector { return &mongodbCollector{} })
}

func (m *mongodbCollector) Name() string { return "mongodb" }

func (m *mongodbCollector) Configure(decode func(v any) error) error {
	if err := decode(&m.cfg); err != nil {
		return err
	}
	if m.cfg.Address == "" {
		m.cfg.Address = "127.0.0.1:27017"
	}
	if m.cfg.Database == "" {
		m.cfg.Database = "admin"
	}
	if m.cfg.Timeout <= 0 {
		m.cfg.Timeout = 2 * time.Second
	}
	return nil
}

func (m *mongodbCollector) Init(reg *registry.Registry) error {
	if m.cfg.Address == "" {
		if err := m.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	if _, err := m.serverStatus(context.Background()); err != nil {
		return err
	}
	inc := registry.Incremental
	for _, c := range []*registry.Chart{
		{ID: "mongodb.operations_by_type_rate", Title: "MongoDB operations by type", Units: "operations/s", Priority: 47000,
			Dimensions: []*registry.Dimension{
				{ID: "insert", Algorithm: inc}, {ID: "query", Algorithm: inc}, {ID: "update", Algorithm: inc},
				{ID: "delete", Algorithm: inc}, {ID: "getmore", Algorithm: inc}, {ID: "command", Algorithm: inc}}},
		{ID: "mongodb.document_operations_rate", Title: "MongoDB document operations", Units: "documents/s", Priority: 47010,
			Dimensions: []*registry.Dimension{
				{ID: "inserted", Algorithm: inc}, {ID: "deleted", Algorithm: inc},
				{ID: "returned", Algorithm: inc}, {ID: "updated", Algorithm: inc}}},
		{ID: "mongodb.connections", Title: "MongoDB connections", Units: "connections", Priority: 47020,
			Dimensions: []*registry.Dimension{{ID: "current"}, {ID: "available"}}},
		{ID: "mongodb.memory", Title: "MongoDB memory", Units: "MiB", Type: registry.Stacked, Priority: 47030,
			Dimensions: []*registry.Dimension{{ID: "resident"}, {ID: "virtual"}}},
		{ID: "mongodb.network_io", Title: "MongoDB network I/O", Units: "bytes/s", Type: registry.Area, Priority: 47040,
			Dimensions: []*registry.Dimension{
				{ID: "in", Algorithm: inc},
				{ID: "out", Algorithm: inc, Multiplier: -1}}},
		{ID: "mongodb.uptime", Title: "MongoDB uptime", Units: "seconds", Priority: 47050,
			Dimensions: []*registry.Dimension{{ID: "uptime"}}},
	} {
		c.Family, c.Plugin, c.Module = "mongodb", "mongodb", "mongodb"
		reg.AddChart(c)
	}
	return nil
}

func (m *mongodbCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	s, err := m.serverStatus(ctx)
	if err != nil {
		return err
	}
	_ = reg.Collect("mongodb.operations_by_type_rate", now, map[string]float64{
		"insert": s["opcounters.insert"], "query": s["opcounters.query"], "update": s["opcounters.update"],
		"delete": s["opcounters.delete"], "getmore": s["opcounters.getmore"], "command": s["opcounters.command"]})
	_ = reg.Collect("mongodb.document_operations_rate", now, map[string]float64{
		"inserted": s["metrics.document.inserted"], "deleted": s["metrics.document.deleted"],
		"returned": s["metrics.document.returned"], "updated": s["metrics.document.updated"]})
	_ = reg.Collect("mongodb.connections", now, map[string]float64{
		"current": s["connections.current"], "available": s["connections.available"]})
	_ = reg.Collect("mongodb.memory", now, map[string]float64{
		"resident": s["mem.resident"], "virtual": s["mem.virtual"]})
	_ = reg.Collect("mongodb.network_io", now, map[string]float64{
		"in": s["network.bytesIn"], "out": s["network.bytesOut"]})
	_ = reg.Collect("mongodb.uptime", now, map[string]float64{"uptime": s["uptime"]})
	return nil
}

func (m *mongodbCollector) serverStatus(ctx context.Context) (map[string]float64, error) {
	d := net.Dialer{Timeout: m.cfg.Timeout}
	conn, err := d.DialContext(ctx, "tcp", m.cfg.Address)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	deadline := time.Now().Add(m.cfg.Timeout)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	_ = conn.SetDeadline(deadline)
	cmd := bsonDoc(bsonInt32("serverStatus", 1), bsonCString("$db", m.cfg.Database))
	if err := mongoWriteOPMSG(conn, 1, cmd); err != nil {
		return nil, err
	}
	doc, err := mongoReadOPMSG(conn)
	if err != nil {
		return nil, err
	}
	out := bsonNumbers(doc, "")
	if ok := out["ok"]; ok == 0 && bsonLookup(doc, "ok") == nil {
		return nil, fmt.Errorf("mongodb: empty serverStatus")
	}
	if errmsg, ok := bsonLookup(doc, "errmsg").(string); ok && errmsg != "" {
		return nil, fmt.Errorf("mongodb: %s", errmsg)
	}
	return out, nil
}

const mongoOPMSG = 2013

func mongoWriteOPMSG(w io.Writer, reqID int32, body []byte) error {
	// header 16 + flags 4 + kind 1 + bson
	payloadLen := 4 + 1 + len(body)
	msg := make([]byte, 16+payloadLen)
	binary.LittleEndian.PutUint32(msg[0:], uint32(len(msg)))
	binary.LittleEndian.PutUint32(msg[4:], uint32(reqID))
	binary.LittleEndian.PutUint32(msg[12:], mongoOPMSG)
	msg[16+4] = 0 // section kind: body
	copy(msg[16+5:], body)
	_, err := w.Write(msg)
	return err
}

func mongoReadOPMSG(r io.Reader) ([]byte, error) {
	var hdr [16]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	n := int(binary.LittleEndian.Uint32(hdr[0:4]))
	if n < 21 || n > 16<<20 {
		return nil, fmt.Errorf("mongodb: bad message length %d", n)
	}
	rest := make([]byte, n-16)
	if _, err := io.ReadFull(r, rest); err != nil {
		return nil, err
	}
	if binary.LittleEndian.Uint32(hdr[12:16]) != mongoOPMSG {
		return nil, fmt.Errorf("mongodb: unexpected opcode %d", binary.LittleEndian.Uint32(hdr[12:16]))
	}
	// rest: flags(4) + sections
	off := 4
	for off < len(rest) {
		kind := rest[off]
		off++
		if kind == 0 {
			if off+4 > len(rest) {
				return nil, fmt.Errorf("mongodb: truncated bson")
			}
			sz := int(binary.LittleEndian.Uint32(rest[off:]))
			if sz < 5 || off+sz > len(rest) {
				return nil, fmt.Errorf("mongodb: bad bson size")
			}
			return rest[off : off+sz], nil
		}
		if kind == 1 {
			if off+4 > len(rest) {
				break
			}
			sz := int(binary.LittleEndian.Uint32(rest[off:]))
			off += sz
			continue
		}
		break
	}
	return nil, fmt.Errorf("mongodb: no body section")
}

func bsonDoc(elems ...[]byte) []byte {
	var b []byte
	for _, e := range elems {
		b = append(b, e...)
	}
	out := make([]byte, 4+len(b)+1)
	binary.LittleEndian.PutUint32(out, uint32(len(out)))
	copy(out[4:], b)
	return out
}

func bsonCString(key, val string) []byte {
	// type 0x02 + key + 0 + int32 strlen+1 + val + 0
	e := append([]byte{0x02}, append([]byte(key), 0)...)
	v := append([]byte(val), 0)
	var ln [4]byte
	binary.LittleEndian.PutUint32(ln[:], uint32(len(v)))
	return append(append(e, ln[:]...), v...)
}

func bsonInt32(key string, v int32) []byte {
	e := append([]byte{0x10}, append([]byte(key), 0)...)
	var n [4]byte
	binary.LittleEndian.PutUint32(n[:], uint32(v))
	return append(e, n[:]...)
}

func bsonDouble(key string, v float64) []byte {
	e := append([]byte{0x01}, append([]byte(key), 0)...)
	var n [8]byte
	binary.LittleEndian.PutUint64(n[:], math.Float64bits(v))
	return append(e, n[:]...)
}

func bsonEmbed(typ byte, key string, doc []byte) []byte {
	e := append([]byte{typ}, append([]byte(key), 0)...)
	return append(e, doc...)
}

func bsonLookup(doc []byte, key string) any {
	m := bsonDecode(doc)
	return m[key]
}

func bsonDecode(doc []byte) map[string]any {
	out := map[string]any{}
	if len(doc) < 5 {
		return out
	}
	end := int(binary.LittleEndian.Uint32(doc))
	if end > len(doc) {
		end = len(doc)
	}
	i := 4
	for i < end-1 {
		typ := doc[i]
		i++
		k0 := i
		for i < end && doc[i] != 0 {
			i++
		}
		if i >= end {
			break
		}
		key := string(doc[k0:i])
		i++
		ni, val, ok := bsonReadValue(doc, i, end, typ)
		if !ok {
			return out
		}
		if val != nil {
			out[key] = val
		}
		i = ni
	}
	return out
}

func bsonReadValue(doc []byte, i, end int, typ byte) (int, any, bool) {
	switch typ {
	case 0x01: // double
		if i+8 > end {
			return i, nil, false
		}
		return i + 8, math.Float64frombits(binary.LittleEndian.Uint64(doc[i : i+8])), true
	case 0x02, 0x0D, 0x0E: // string / code / symbol
		if i+4 > end {
			return i, nil, false
		}
		n := int(binary.LittleEndian.Uint32(doc[i:]))
		i += 4
		if n < 1 || i+n > end {
			return i, nil, false
		}
		s := string(doc[i : i+n-1])
		return i + n, s, true
	case 0x03, 0x04: // document / array
		if i+4 > end {
			return i, nil, false
		}
		n := int(binary.LittleEndian.Uint32(doc[i:]))
		if n < 5 || i+n > end {
			return i, nil, false
		}
		return i + n, bsonDecode(doc[i : i+n]), true
	case 0x05: // binary
		if i+4 > end {
			return i, nil, false
		}
		n := int(binary.LittleEndian.Uint32(doc[i:]))
		i += 5 + n // length + subtype + data
		if i > end {
			return i, nil, false
		}
		return i, nil, true
	case 0x06, 0x0A, 0xFF, 0x7F: // undefined / null / min / max
		return i, nil, true
	case 0x07: // ObjectId
		if i+12 > end {
			return i, nil, false
		}
		return i + 12, nil, true
	case 0x08: // bool
		if i >= end {
			return i, nil, false
		}
		return i + 1, doc[i] != 0, true
	case 0x09, 0x11, 0x12: // datetime / timestamp / int64
		if i+8 > end {
			return i, nil, false
		}
		if typ == 0x12 {
			return i + 8, int64(binary.LittleEndian.Uint64(doc[i:])), true
		}
		return i + 8, nil, true
	case 0x10: // int32
		if i+4 > end {
			return i, nil, false
		}
		return i + 4, int32(binary.LittleEndian.Uint32(doc[i:])), true
	case 0x0B: // regex: two cstrings
		for n := 0; n < 2; n++ {
			for i < end && doc[i] != 0 {
				i++
			}
			if i >= end {
				return i, nil, false
			}
			i++
		}
		return i, nil, true
	case 0x0F: // code with scope
		if i+4 > end {
			return i, nil, false
		}
		n := int(binary.LittleEndian.Uint32(doc[i:]))
		if n < 5 || i+n > end {
			return i, nil, false
		}
		return i + n, nil, true
	case 0x13: // decimal128
		if i+16 > end {
			return i, nil, false
		}
		return i + 16, nil, true
	default:
		return i, nil, false
	}
}

func bsonNumbers(v any, prefix string) map[string]float64 {
	out := map[string]float64{}
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			p := k
			if prefix != "" {
				p = prefix + "." + k
			}
			for kk, vv := range bsonNumbers(val, p) {
				out[kk] = vv
			}
		}
	case float64:
		out[prefix] = t
	case int32:
		out[prefix] = float64(t)
	case int64:
		out[prefix] = float64(t)
	case bool:
		if t {
			out[prefix] = 1
		} else {
			out[prefix] = 0
		}
	}
	return out
}
