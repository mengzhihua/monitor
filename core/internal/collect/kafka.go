package collect

import (
	"context"
	"fmt"
	"time"

	"github.com/mengzhihua/monitor/core/internal/kafka"
	"github.com/mengzhihua/monitor/core/internal/registry"
)

// kafkaConfig is collectors.modules.kafka. Brokers are host:port or kafka://host:port/topic.
// The collector speaks the Kafka metadata protocol and disables itself when no broker answers.
type kafkaConfig struct {
	Brokers []string      `yaml:"brokers"`
	Topic   string        `yaml:"topic"`
	Timeout time.Duration `yaml:"timeout"`
}

type kafkaCollector struct {
	cfg   kafkaConfig
	addr  string
	topic string
	meta  func(addr, topic string, timeout time.Duration) (int, error)
}

func init() {
	Register("kafka", func() Collector { return &kafkaCollector{} })
}

func (k *kafkaCollector) Name() string { return "kafka" }

func (k *kafkaCollector) Configure(decode func(v any) error) error {
	if err := decode(&k.cfg); err != nil {
		return err
	}
	if k.cfg.Timeout <= 0 {
		k.cfg.Timeout = 3 * time.Second
	}
	if k.cfg.Topic == "" {
		k.cfg.Topic = "monitor"
	}
	return nil
}

func (k *kafkaCollector) Init(reg *registry.Registry) error {
	if k.meta == nil {
		k.meta = kafka.Metadata
	}
	if len(k.cfg.Brokers) == 0 {
		return fmt.Errorf("kafka: no brokers")
	}
	addr, topic, ok := kafka.Broker(k.cfg.Brokers[0])
	if !ok {
		return fmt.Errorf("kafka: bad broker %q", k.cfg.Brokers[0])
	}
	if k.cfg.Topic != "" && topic == "monitor" {
		topic = k.cfg.Topic
	}
	n, err := k.meta(addr, topic, k.cfg.Timeout)
	if err != nil {
		return fmt.Errorf("kafka: %w", err)
	}
	k.addr, k.topic = addr, topic
	ch := sysChart("kafka.broker.partitions", "kafka", "Kafka topic partitions", "partitions", 61000,
		&registry.Dimension{ID: "partitions"})
	ch.Plugin, ch.Module = "kafka", "kafka"
	reg.AddChart(ch)
	_ = n
	return nil
}

func (k *kafkaCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	n, err := k.meta(k.addr, k.topic, k.cfg.Timeout)
	if err != nil {
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	_ = reg.Collect("kafka.broker.partitions", now, map[string]float64{"partitions": float64(n)})
	return nil
}
