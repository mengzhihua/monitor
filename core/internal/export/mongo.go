package export

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"net"
	"time"
)

const mongoOpMsg = 2013

func (e *Engine) flushMongo(ctx context.Context, d Destination) error {
	addr := d.Address
	if addr == "" {
		addr = d.URL
	}
	if addr == "" {
		return fmt.Errorf("mongodb: address required")
	}
	db := d.Database
	if db == "" {
		db = "netdata"
	}
	coll := d.Collection
	if coll == "" {
		coll = "metrics"
	}
	var docs [][]byte
	for _, p := range e.snapshot() {
		docs = append(docs, bsonDoc(
			bsonCString("hostname", e.host),
			bsonCString("prefix", d.Prefix),
			bsonCString("chart", p.chart),
			bsonCString("dimension", p.dim),
			bsonDouble("value", p.value),
			bsonInt64("timestamp", p.ts),
		))
	}
	if len(docs) == 0 {
		return nil
	}
	dialer := net.Dialer{Timeout: 5 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
	const batch = 256
	for i := 0; i < len(docs); i += batch {
		end := i + batch
		if end > len(docs) {
			end = len(docs)
		}
		if err := mongoInsert(conn, db, coll, docs[i:end]); err != nil {
			return err
		}
	}
	return nil
}

func mongoInsert(conn net.Conn, db, coll string, docs [][]byte) error {
	elems := make([][]byte, 0, len(docs))
	for i, d := range docs {
		elems = append(elems, bsonEmbed(0x03, fmt.Sprint(i), d))
	}
	cmd := bsonDoc(
		bsonCString("insert", coll),
		bsonCString("$db", db),
		bsonEmbed(0x04, "documents", bsonDoc(elems...)),
	)
	payload := append([]byte{0}, cmd...) // section kind 0
	msg := make([]byte, 20+len(payload))
	binary.LittleEndian.PutUint32(msg[0:4], uint32(len(msg)))
	binary.LittleEndian.PutUint32(msg[4:8], 1)
	binary.LittleEndian.PutUint32(msg[12:16], mongoOpMsg)
	copy(msg[20:], payload)
	if _, err := conn.Write(msg); err != nil {
		return err
	}
	var hdr [16]byte
	if _, err := io.ReadFull(conn, hdr[:]); err != nil {
		return err
	}
	n := binary.LittleEndian.Uint32(hdr[0:4])
	if n < 16 || n > 16<<20 {
		return fmt.Errorf("mongodb: bad reply length %d", n)
	}
	rest := make([]byte, n-16)
	_, err := io.ReadFull(conn, rest)
	return err
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
	e := append([]byte{0x02}, append([]byte(key), 0)...)
	v := append([]byte(val), 0)
	var ln [4]byte
	binary.LittleEndian.PutUint32(ln[:], uint32(len(v)))
	return append(append(e, ln[:]...), v...)
}

func bsonDouble(key string, v float64) []byte {
	e := append([]byte{0x01}, append([]byte(key), 0)...)
	var n [8]byte
	binary.LittleEndian.PutUint64(n[:], math.Float64bits(v))
	return append(e, n[:]...)
}

func bsonInt64(key string, v int64) []byte {
	e := append([]byte{0x12}, append([]byte(key), 0)...)
	var n [8]byte
	binary.LittleEndian.PutUint64(n[:], uint64(v))
	return append(e, n[:]...)
}

func bsonEmbed(typ byte, key string, doc []byte) []byte {
	e := append([]byte{typ}, append([]byte(key), 0)...)
	return append(e, doc...)
}
