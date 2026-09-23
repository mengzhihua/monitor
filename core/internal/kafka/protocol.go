// Package kafka speaks a small slice of the Kafka binary protocol: metadata
// queries and produce requests. HTTP Kafka REST stays available for proxies.
package kafka

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"io"
	"net"
	"strings"
	"time"
)

// Broker splits kafka://host:port/topic or host:port.
func Broker(raw string) (addr, topic string, ok bool) {
	s := strings.TrimSpace(raw)
	s = strings.TrimPrefix(s, "kafka://")
	if s == "" || strings.Contains(s, "://") {
		return "", "", false
	}
	topic = "monitor"
	if i := strings.IndexByte(s, '/'); i >= 0 {
		topic = strings.Trim(s[i+1:], "/")
		s = s[:i]
		if topic == "" {
			topic = "monitor"
		}
	}
	if !strings.Contains(s, ":") {
		s += ":9092"
	}
	return s, topic, true
}

// Metadata asks for one topic and returns the partition count.
func Metadata(addr, topic string, timeout time.Duration) (int, error) {
	body := encodeString(topic)
	// topics array length 1, then one name. API version 1 has no allow_auto_topic.
	payload := append([]byte{0, 0, 0, 1}, body...)
	resp, err := roundTrip(addr, 3, 1, payload, timeout)
	if err != nil {
		return 0, err
	}
	// brokers array, controller id, topics array
	off := 0
	off, _, err = skipArray(resp, off, skipBroker)
	if err != nil {
		return 0, err
	}
	if off+4 > len(resp) {
		return 0, fmt.Errorf("kafka: short metadata")
	}
	off += 4 // controller id
	if off+4 > len(resp) {
		return 0, fmt.Errorf("kafka: short topics")
	}
	n := int(int32(binary.BigEndian.Uint32(resp[off:])))
	off += 4
	if n <= 0 {
		return 0, fmt.Errorf("kafka: no topics")
	}
	off, err = skipString(resp, off) // error? topic error is int16 before name on v1
	// Metadata v1 topic: error_code int16, name string, [partitions]
	// I skipped the name too early. Re-parse the first topic properly.
	_ = off
	return partitionsV1(resp)
}

func partitionsV1(resp []byte) (int, error) {
	off := 0
	var err error
	off, _, err = skipArray(resp, off, skipBroker)
	if err != nil {
		return 0, err
	}
	if off+4 > len(resp) {
		return 0, fmt.Errorf("kafka: short metadata")
	}
	off += 4
	if off+4 > len(resp) {
		return 0, fmt.Errorf("kafka: short topics")
	}
	n := int(int32(binary.BigEndian.Uint32(resp[off:])))
	off += 4
	if n < 1 {
		return 0, fmt.Errorf("kafka: no topics")
	}
	if off+2 > len(resp) {
		return 0, fmt.Errorf("kafka: short topic error")
	}
	code := int16(binary.BigEndian.Uint16(resp[off:]))
	off += 2
	if code != 0 {
		return 0, fmt.Errorf("kafka: topic error %d", code)
	}
	off, err = skipString(resp, off)
	if err != nil {
		return 0, err
	}
	if off+4 > len(resp) {
		return 0, fmt.Errorf("kafka: short partitions")
	}
	return int(int32(binary.BigEndian.Uint32(resp[off:]))), nil
}

func skipBroker(b []byte, off int) (int, error) {
	off += 4 // node id
	off, err := skipString(b, off)
	if err != nil {
		return 0, err
	}
	off, err = skipString(b, off)
	if err != nil {
		return 0, err
	}
	if off+4 > len(b) {
		return 0, fmt.Errorf("kafka: short broker")
	}
	return off + 4, nil // port
}

func skipArray(b []byte, off int, skip func([]byte, int) (int, error)) (int, int, error) {
	if off+4 > len(b) {
		return 0, 0, fmt.Errorf("kafka: short array")
	}
	n := int(int32(binary.BigEndian.Uint32(b[off:])))
	off += 4
	if n < 0 {
		return off, 0, nil
	}
	for i := 0; i < n; i++ {
		var err error
		off, err = skip(b, off)
		if err != nil {
			return 0, 0, err
		}
	}
	return off, n, nil
}

func skipString(b []byte, off int) (int, error) {
	if off+2 > len(b) {
		return 0, fmt.Errorf("kafka: short string")
	}
	n := int(int16(binary.BigEndian.Uint16(b[off:])))
	off += 2
	if n < 0 {
		return off, nil
	}
	if off+n > len(b) {
		return 0, fmt.Errorf("kafka: string overrun")
	}
	return off + n, nil
}

// Produce sends one JSON-ish record with acks=1 using a magic-1 message set.
func Produce(addr, topic string, value []byte, timeout time.Duration) error {
	msg := messageV1(value)
	part := make([]byte, 0, 8+len(msg))
	part = append(part, 0, 0, 0, 0) // partition 0
	part = binary.BigEndian.AppendUint32(part, uint32(len(msg)))
	part = append(part, msg...)
	var body []byte
	body = append(body, 0, 0)             // transactional id null? v2 has no transactional id. Use v2.
	body = append(body, 0, 1)             // acks = 1
	body = append(body, 0, 0, 0x13, 0x88) // timeout 5000
	body = append(body, 0, 0, 0, 1)       // one topic
	body = append(body, encodeString(topic)...)
	body = append(body, 0, 0, 0, 1) // one partition
	body = append(body, part...)
	// The leading null string above was wrong for v2. Rebuild.
	body = body[:0]
	body = append(body, 0, 1) // acks
	body = binary.BigEndian.AppendUint32(body, 5000)
	body = binary.BigEndian.AppendUint32(body, 1)
	body = append(body, encodeString(topic)...)
	body = binary.BigEndian.AppendUint32(body, 1)
	body = append(body, part...)
	resp, err := roundTrip(addr, 0, 2, body, timeout)
	if err != nil {
		return err
	}
	if len(resp) < 10 {
		return fmt.Errorf("kafka: short produce response")
	}
	// [topics] name [partitions] index error_code offset, then throttle on v2? v2 has throttle at end.
	off := 0
	if off+4 > len(resp) {
		return fmt.Errorf("kafka: short produce topics")
	}
	off += 4
	off, err = skipString(resp, off)
	if err != nil {
		return err
	}
	if off+4 > len(resp) {
		return fmt.Errorf("kafka: short produce partitions")
	}
	off += 4
	if off+6 > len(resp) {
		return fmt.Errorf("kafka: short produce partition")
	}
	off += 4 // index
	code := int16(binary.BigEndian.Uint16(resp[off:]))
	if code != 0 {
		return fmt.Errorf("kafka: produce error %d", code)
	}
	return nil
}

func messageV1(value []byte) []byte {
	// offset(8) + size(4) + crc(4) + magic + attr + timestamp(8) + key(-1) + value
	inner := make([]byte, 0, 1+1+8+4+4+len(value))
	inner = append(inner, 1, 0) // magic, attributes
	inner = binary.BigEndian.AppendUint64(inner, uint64(time.Now().UnixMilli()))
	inner = append(inner, 0xff, 0xff, 0xff, 0xff) // null key
	inner = binary.BigEndian.AppendUint32(inner, uint32(len(value)))
	inner = append(inner, value...)
	crc := crc32.ChecksumIEEE(inner)
	msg := make([]byte, 0, 8+4+4+len(inner))
	msg = append(msg, 0, 0, 0, 0, 0, 0, 0, 0) // offset
	msg = binary.BigEndian.AppendUint32(msg, uint32(4+len(inner)))
	msg = binary.BigEndian.AppendUint32(msg, crc)
	msg = append(msg, inner...)
	return msg
}

func encodeString(s string) []byte {
	b := make([]byte, 2+len(s))
	binary.BigEndian.PutUint16(b, uint16(len(s)))
	copy(b[2:], s)
	return b
}

func roundTrip(addr string, apiKey, apiVer int16, body []byte, timeout time.Duration) ([]byte, error) {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	d := net.Dialer{Timeout: timeout}
	conn, err := d.Dial("tcp", addr)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	req := make([]byte, 0, 14+len(body))
	req = binary.BigEndian.AppendUint16(req, uint16(apiKey))
	req = binary.BigEndian.AppendUint16(req, uint16(apiVer))
	req = binary.BigEndian.AppendUint32(req, 1) // correlation
	req = append(req, encodeString("monitor")...)
	req = append(req, body...)
	frame := binary.BigEndian.AppendUint32(nil, uint32(len(req)))
	frame = append(frame, req...)
	if _, err := conn.Write(frame); err != nil {
		return nil, err
	}
	var sz [4]byte
	if _, err := io.ReadFull(conn, sz[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(sz[:])
	if n < 4 || n > 8<<20 {
		return nil, fmt.Errorf("kafka: bad frame %d", n)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(conn, buf); err != nil {
		return nil, err
	}
	if binary.BigEndian.Uint32(buf[:4]) != 1 {
		return nil, fmt.Errorf("kafka: correlation")
	}
	return buf[4:], nil
}
