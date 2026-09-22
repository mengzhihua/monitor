package stream

import (
	"testing"
)

func TestMQTTConnectPublishRoundTrip(t *testing.T) {
	raw := EncodeMQTTConnect(MQTTConnect{ClientID: "agent-1", Username: "k1", Password: "secret", KeepAlive: 60})
	pkt, err := ParseMQTTPacket(raw)
	if err != nil {
		t.Fatal(err)
	}
	c, err := DecodeMQTTConnect(pkt)
	if err != nil {
		t.Fatal(err)
	}
	if c.ClientID != "agent-1" || c.Username != "k1" || c.Password != "secret" || c.KeepAlive != 60 {
		t.Fatalf("%+v", c)
	}

	pub := EncodeMQTTPublish("agent/n1/from-agent", []byte(`{"type":"hello"}`))
	pp, err := ParseMQTTPacket(pub)
	if err != nil {
		t.Fatal(err)
	}
	topic, payload, err := DecodeMQTTPublish(pp)
	if err != nil {
		t.Fatal(err)
	}
	if topic != "agent/n1/from-agent" || string(payload) != `{"type":"hello"}` {
		t.Fatalf("%s %s", topic, payload)
	}

	sub := EncodeMQTTSubscribe(7, "agent/n1/from-cloud")
	sp, err := ParseMQTTPacket(sub)
	if err != nil {
		t.Fatal(err)
	}
	id, topic, err := DecodeMQTTSubscribe(sp)
	if err != nil || id != 7 || topic != "agent/n1/from-cloud" {
		t.Fatalf("%d %s %v", id, topic, err)
	}

	ack := EncodeMQTTConnack(0)
	ap, err := ParseMQTTPacket(ack)
	if err != nil || ap.Type != mqttConnack || len(ap.Payload) < 2 || ap.Payload[1] != 0 {
		t.Fatalf("connack %+v %v", ap, err)
	}

	ping := EncodeMQTTPingreq()
	ppkt, err := ParseMQTTPacket(ping)
	if err != nil || ppkt.Type != mqttPingreq {
		t.Fatalf("pingreq %+v %v", ppkt, err)
	}
}
