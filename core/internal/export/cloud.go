package export

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/kafka"
)

// flushKinesis POSTs JSON records to a Kinesis Data Streams / Firehose HTTP
// endpoint (or a compatible proxy). Destination.URL is the PutRecord(s) URL.
func (e *Engine) flushKinesis(ctx context.Context, d Destination) error {
	url := d.URL
	if url == "" {
		return fmt.Errorf("kinesis: url required")
	}
	var recs []map[string]any
	for _, p := range e.snapshot() {
		recs = append(recs, map[string]any{
			"Data": map[string]any{"host": e.host, "prefix": d.Prefix, "chart": p.chart, "dimension": p.dim, "value": p.value, "timestamp": p.ts},
		})
	}
	body, err := json.Marshal(map[string]any{"StreamName": d.Prefix, "Records": recs})
	if err != nil {
		return err
	}
	return e.post(ctx, url, "application/json", body, d.Headers)
}

// flushPubSub POSTs to the Google Pub/Sub publish REST API
// (https://pubsub.googleapis.com/v1/projects/.../topics/...:publish).
func (e *Engine) flushPubSub(ctx context.Context, d Destination) error {
	url := d.URL
	if url == "" {
		return fmt.Errorf("pubsub: url required")
	}
	var msgs []map[string]any
	for _, p := range e.snapshot() {
		payload, _ := json.Marshal(map[string]any{
			"host": e.host, "prefix": d.Prefix, "chart": p.chart, "dimension": p.dim, "value": p.value, "timestamp": p.ts,
		})
		msgs = append(msgs, map[string]any{"data": payload, "attributes": map[string]string{"host": e.host, "chart": p.chart}})
	}
	body, err := json.Marshal(map[string]any{"messages": msgs})
	if err != nil {
		return err
	}
	return e.post(ctx, url, "application/json", body, d.Headers)
}

// flushKafka POSTs Kafka REST proxy JSON (application/vnd.kafka.json.v2+json).
func (e *Engine) flushKafka(ctx context.Context, d Destination) error {
	raw := d.URL
	if raw == "" {
		raw = d.Address
	}
	if addr, topic, ok := kafka.Broker(raw); ok && !strings.HasPrefix(raw, "http") {
		return e.flushKafkaProtocol(addr, topic, d)
	}
	url := d.URL
	if url == "" {
		return fmt.Errorf("kafka: url required")
	}
	var recs []map[string]any
	for _, p := range e.snapshot() {
		recs = append(recs, map[string]any{
			"value": map[string]any{"host": e.host, "prefix": d.Prefix, "chart": p.chart, "dimension": p.dim, "value": p.value, "timestamp": p.ts},
		})
	}
	body, err := json.Marshal(map[string]any{"records": recs})
	if err != nil {
		return err
	}
	return e.post(ctx, url, "application/vnd.kafka.json.v2+json", body, d.Headers)
}

func (e *Engine) flushKafkaProtocol(addr, topic string, d Destination) error {
	var recs []map[string]any
	for _, p := range e.snapshot() {
		recs = append(recs, map[string]any{
			"host": e.host, "prefix": d.Prefix, "chart": p.chart, "dimension": p.dim, "value": p.value, "timestamp": p.ts,
		})
	}
	body, err := json.Marshal(map[string]any{"records": recs})
	if err != nil {
		return err
	}
	return kafka.Produce(addr, topic, body, 5*time.Second)
}
