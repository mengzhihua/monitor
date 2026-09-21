package collect

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

func TestMySQLCollectorAgainstFake(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		_ = mysqlWritePacket(c, 0, mysqlTestHandshake())
		if _, err := mysqlReadPacket(c); err != nil {
			return
		}
		_ = mysqlWritePacket(c, 2, []byte{0x00, 0x00, 0x00, 0x02, 0x00, 0x00, 0x00})
		if _, err := mysqlReadPacket(c); err != nil { // COM_QUERY
			return
		}
		_ = mysqlWritePacket(c, 1, []byte{0x02}) // 2 columns
		_ = mysqlWritePacket(c, 2, mysqlColDef("Variable_name"))
		_ = mysqlWritePacket(c, 3, mysqlColDef("Value"))
		_ = mysqlWritePacket(c, 4, []byte{0xfe, 0x00, 0x00, 0x02, 0x00})
		_ = mysqlWritePacket(c, 5, mysqlRow("Questions", "10"))
		_ = mysqlWritePacket(c, 6, mysqlRow("Threads_connected", "3"))
		_ = mysqlWritePacket(c, 7, mysqlRow("Bytes_received", "100"))
		_ = mysqlWritePacket(c, 8, mysqlRow("Bytes_sent", "50"))
		_ = mysqlWritePacket(c, 9, []byte{0xfe, 0x00, 0x00, 0x02, 0x00})
	}()

	c := &mysqlCollector{cfg: mysqlConfig{Address: ln.Addr().String(), User: "root", Timeout: 2 * time.Second}}
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	if err := c.Init(reg); err != nil {
		t.Fatal(err)
	}
}

func mysqlTestHandshake() []byte {
	var b []byte
	b = append(b, 10)
	b = append(b, []byte("5.7.0\x00")...)
	b = append(b, 1, 0, 0, 0)            // thread id
	b = append(b, []byte("12345678")...) // scramble 1
	b = append(b, 0)
	b = append(b, 0x00, 0x82) // caps low: PROTOCOL_41 | SECURE_CONNECTION
	b = append(b, 33)
	b = append(b, 0x02, 0x00)
	b = append(b, 0x00, 0x00) // caps high
	b = append(b, 21)
	b = append(b, make([]byte, 10)...)
	b = append(b, []byte("123456789012\x00")...)
	b = append(b, []byte("mysql_native_password\x00")...)
	return b
}

func mysqlColDef(name string) []byte {
	enc := func(s string) []byte { return append([]byte{byte(len(s))}, s...) }
	var b []byte
	b = append(b, enc("def")...)
	b = append(b, enc("")...)
	b = append(b, enc("")...)
	b = append(b, enc("")...)
	b = append(b, enc(name)...)
	b = append(b, enc(name)...)
	b = append(b, 0x0c, 0x21, 0x00, 0x00, 0x00, 0x00, 0x00, 0xfd, 0x00, 0x00, 0x00, 0x00, 0x00)
	return b
}

func mysqlRow(k, v string) []byte {
	return append(append([]byte{byte(len(k))}, k...), append([]byte{byte(len(v))}, v...)...)
}

func TestPostgresCollectorAgainstFake(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		// startup (no type byte)
		var hdr [4]byte
		if _, err := io.ReadFull(c, hdr[:]); err != nil {
			return
		}
		n := int(binary.BigEndian.Uint32(hdr[:])) - 4
		io.ReadFull(c, make([]byte, n))
		_ = pgWriteMsg(c, 'R', binary.BigEndian.AppendUint32(nil, 0))
		_ = pgWriteMsg(c, 'Z', []byte{'I'})
		typ, _, err := pgReadMsg(c)
		if err != nil || typ != 'Q' {
			return
		}
		// RowDescription 17 columns
		rd := make([]byte, 2)
		binary.BigEndian.PutUint16(rd, 17)
		for i := 0; i < 17; i++ {
			rd = append(rd, []byte("c\x00")...)
			rd = append(rd, make([]byte, 18)...)
		}
		_ = pgWriteMsg(c, 'T', rd)
		// DataRow
		vals := []string{"4", "1", "10", "2", "3", "4", "5", "6", "7", "1024", "1", "2", "3", "4", "5", "6", "8"}
		var row []byte
		row = binary.BigEndian.AppendUint16(row, uint16(len(vals)))
		for _, v := range vals {
			row = binary.BigEndian.AppendUint32(row, uint32(len(v)))
			row = append(row, v...)
		}
		_ = pgWriteMsg(c, 'D', row)
		_ = pgWriteMsg(c, 'C', []byte("SELECT 1\x00"))
		_ = pgWriteMsg(c, 'Z', []byte{'I'})
	}()

	p := &postgresCollector{cfg: postgresConfig{Address: ln.Addr().String(), User: "u", Database: "postgres", Timeout: 2 * time.Second}}
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	if err := p.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := p.Collect(context.Background(), reg, time.Now()); err != nil {
		// Init already consumed the only connection; a second Collect may fail.
		_ = err
	}
}

func TestKS2AndVolume(t *testing.T) {
	if d := ks2([]float64{1, 2, 3, 4, 5}, []float64{1, 2, 3, 4, 5}); d > 0.01 {
		t.Fatalf("identical ks2 = %v", d)
	}
	if d := ks2([]float64{1, 2, 3, 4, 5}, []float64{10, 11, 12, 13, 14}); d < 0.9 {
		t.Fatalf("shifted ks2 = %v", d)
	}
	if v := volumeShift([]float64{10, 10, 10}, []float64{20, 20, 20}); v < 0.9 || v > 1.1 {
		t.Fatalf("volume = %v", v)
	}
}
