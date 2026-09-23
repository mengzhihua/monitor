package kafka

import (
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"
)

func TestBroker(t *testing.T) {
	addr, topic, ok := Broker("kafka://127.0.0.1:9092/metrics")
	if !ok || addr != "127.0.0.1:9092" || topic != "metrics" {
		t.Fatalf("%s %s %v", addr, topic, ok)
	}
	if _, _, ok := Broker("http://example/topics/x"); ok {
		t.Fatal("http is not the native scheme")
	}
}

func TestProduceRoundTrip(t *testing.T) {
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
		var sz [4]byte
		if _, err := io.ReadFull(c, sz[:]); err != nil {
			return
		}
		buf := make([]byte, binary.BigEndian.Uint32(sz[:]))
		if _, err := io.ReadFull(c, buf); err != nil {
			return
		}
		if binary.BigEndian.Uint16(buf[0:2]) != 0 {
			return
		}
		// correlation is at offset 4, client string follows. Reply with one topic.
		resp := []byte{0, 0, 0, 1} // correlation
		resp = append(resp, 0, 0, 0, 1)
		resp = append(resp, encodeString("metrics")...)
		resp = append(resp, 0, 0, 0, 1)
		resp = append(resp, 0, 0, 0, 0)             // partition
		resp = append(resp, 0, 0)                   // error
		resp = append(resp, 0, 0, 0, 0, 0, 0, 0, 1) // offset
		resp = append(resp, 0, 0, 0, 0)             // throttle
		frame := binary.BigEndian.AppendUint32(nil, uint32(len(resp)))
		frame = append(frame, resp...)
		_, _ = c.Write(frame)
	}()
	if err := Produce(ln.Addr().String(), "metrics", []byte(`{"v":1}`), time.Second); err != nil {
		t.Fatal(err)
	}
}
