package stream

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"
)

// Minimal MQTT 3.1.1 codec for ACLK (CONNECT/CONNACK/PUBLISH/SUBSCRIBE/PING/DISCONNECT).
const (
	mqttConnect     = 1
	mqttConnack     = 2
	mqttPublish     = 3
	mqttSubscribe   = 8
	mqttSuback      = 9
	mqttPingreq     = 12
	mqttPingresp    = 13
	mqttDisconnect  = 14
	mqttProtocol311 = 4
)

// MQTTPacket is one decoded control packet.
type MQTTPacket struct {
	Type     byte
	Flags    byte
	Payload  []byte
	Topic    string // PUBLISH
	PacketID uint16
}

func mqttEncodeRemaining(n int) []byte {
	var out []byte
	for {
		b := byte(n % 128)
		n /= 128
		if n > 0 {
			b |= 0x80
		}
		out = append(out, b)
		if n == 0 {
			break
		}
	}
	return out
}

func mqttAppendUTF8(dst []byte, s string) []byte {
	n := len(s)
	dst = append(dst, byte(n>>8), byte(n))
	return append(dst, s...)
}

func mqttReadUTF8(p []byte) (string, []byte, error) {
	if len(p) < 2 {
		return "", nil, io.ErrUnexpectedEOF
	}
	n := int(p[0])<<8 | int(p[1])
	p = p[2:]
	if len(p) < n {
		return "", nil, io.ErrUnexpectedEOF
	}
	s := string(p[:n])
	if !utf8.ValidString(s) {
		return "", nil, errors.New("mqtt: invalid utf-8")
	}
	return s, p[n:], nil
}

func mqttWrap(typ, flags byte, payload []byte) []byte {
	out := []byte{(typ << 4) | flags}
	out = append(out, mqttEncodeRemaining(len(payload))...)
	return append(out, payload...)
}

// MQTTConnect is the CONNECT variable header + payload we care about.
type MQTTConnect struct {
	ClientID  string
	Username  string
	Password  string
	KeepAlive uint16
}

func EncodeMQTTConnect(c MQTTConnect) []byte {
	var vh []byte
	vh = mqttAppendUTF8(vh, "MQTT")
	vh = append(vh, mqttProtocol311)
	flags := byte(0x02) // clean session
	if c.Username != "" {
		flags |= 0x80
	}
	if c.Password != "" {
		flags |= 0x40
	}
	vh = append(vh, flags)
	ka := make([]byte, 2)
	binary.BigEndian.PutUint16(ka, c.KeepAlive)
	vh = append(vh, ka...)
	vh = mqttAppendUTF8(vh, c.ClientID)
	if c.Username != "" {
		vh = mqttAppendUTF8(vh, c.Username)
	}
	if c.Password != "" {
		vh = mqttAppendUTF8(vh, c.Password)
	}
	return mqttWrap(mqttConnect, 0, vh)
}

func DecodeMQTTConnect(pkt MQTTPacket) (MQTTConnect, error) {
	if pkt.Type != mqttConnect {
		return MQTTConnect{}, fmt.Errorf("mqtt: expected CONNECT, got %d", pkt.Type)
	}
	p := pkt.Payload
	name, p, err := mqttReadUTF8(p)
	if err != nil {
		return MQTTConnect{}, err
	}
	if name != "MQTT" && name != "MQIsdp" {
		return MQTTConnect{}, fmt.Errorf("mqtt: protocol %q", name)
	}
	if len(p) < 4 {
		return MQTTConnect{}, io.ErrUnexpectedEOF
	}
	_ = p[0] // level
	flags := p[1]
	ka := binary.BigEndian.Uint16(p[2:4])
	p = p[4:]
	cid, p, err := mqttReadUTF8(p)
	if err != nil {
		return MQTTConnect{}, err
	}
	out := MQTTConnect{ClientID: cid, KeepAlive: ka}
	if flags&0x04 != 0 { // will
		_, p, err = mqttReadUTF8(p)
		if err != nil {
			return MQTTConnect{}, err
		}
		_, p, err = mqttReadUTF8(p)
		if err != nil {
			return MQTTConnect{}, err
		}
	}
	if flags&0x80 != 0 {
		out.Username, p, err = mqttReadUTF8(p)
		if err != nil {
			return MQTTConnect{}, err
		}
	}
	if flags&0x40 != 0 {
		out.Password, _, err = mqttReadUTF8(p)
		if err != nil {
			return MQTTConnect{}, err
		}
	}
	return out, nil
}

func EncodeMQTTConnack(returnCode byte) []byte {
	return mqttWrap(mqttConnack, 0, []byte{0, returnCode})
}

func EncodeMQTTPublish(topic string, payload []byte) []byte {
	var p []byte
	p = mqttAppendUTF8(p, topic)
	p = append(p, payload...)
	return mqttWrap(mqttPublish, 0, p) // QoS 0
}

func DecodeMQTTPublish(pkt MQTTPacket) (topic string, payload []byte, err error) {
	if pkt.Type != mqttPublish {
		return "", nil, fmt.Errorf("mqtt: expected PUBLISH, got %d", pkt.Type)
	}
	topic, rest, err := mqttReadUTF8(pkt.Payload)
	if err != nil {
		return "", nil, err
	}
	qos := (pkt.Flags >> 1) & 0x03
	if qos > 0 {
		if len(rest) < 2 {
			return "", nil, io.ErrUnexpectedEOF
		}
		rest = rest[2:]
	}
	return topic, rest, nil
}

func EncodeMQTTSubscribe(packetID uint16, topic string) []byte {
	p := make([]byte, 2)
	binary.BigEndian.PutUint16(p, packetID)
	p = mqttAppendUTF8(p, topic)
	p = append(p, 0) // QoS 0
	return mqttWrap(mqttSubscribe, 0x02, p)
}

func DecodeMQTTSubscribe(pkt MQTTPacket) (packetID uint16, topic string, err error) {
	if pkt.Type != mqttSubscribe {
		return 0, "", fmt.Errorf("mqtt: expected SUBSCRIBE, got %d", pkt.Type)
	}
	if len(pkt.Payload) < 2 {
		return 0, "", io.ErrUnexpectedEOF
	}
	packetID = binary.BigEndian.Uint16(pkt.Payload[:2])
	topic, rest, err := mqttReadUTF8(pkt.Payload[2:])
	if err != nil {
		return 0, "", err
	}
	_ = rest
	return packetID, topic, nil
}

func EncodeMQTTSuback(packetID uint16) []byte {
	p := make([]byte, 2)
	binary.BigEndian.PutUint16(p, packetID)
	p = append(p, 0) // QoS 0 granted
	return mqttWrap(mqttSuback, 0, p)
}

func EncodeMQTTPingreq() []byte  { return mqttWrap(mqttPingreq, 0, nil) }
func EncodeMQTTPingresp() []byte { return mqttWrap(mqttPingresp, 0, nil) }

func ParseMQTTPacket(b []byte) (MQTTPacket, error) {
	if len(b) < 2 {
		return MQTTPacket{}, io.ErrUnexpectedEOF
	}
	typ := b[0] >> 4
	flags := b[0] & 0x0f
	n, size, err := mqttDecodeRemaining(b[1:])
	if err != nil {
		return MQTTPacket{}, err
	}
	start := 1 + size
	if len(b) < start+n {
		return MQTTPacket{}, io.ErrUnexpectedEOF
	}
	return MQTTPacket{Type: typ, Flags: flags, Payload: b[start : start+n]}, nil
}

func mqttDecodeRemaining(p []byte) (int, int, error) {
	var n, m, i int
	for {
		if i >= len(p) {
			return 0, 0, io.ErrUnexpectedEOF
		}
		b := p[i]
		i++
		n += int(b&0x7f) * (1 << (m * 7))
		m++
		if b&0x80 == 0 {
			return n, i, nil
		}
		if m > 4 {
			return 0, 0, errors.New("mqtt: remaining length overflow")
		}
	}
}
