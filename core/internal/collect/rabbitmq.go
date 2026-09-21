package collect

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// rabbitmqConfig is collectors.modules.rabbitmq (management API).
type rabbitmqConfig struct {
	URL      string        `yaml:"url"`
	User     string        `yaml:"user"`
	Password string        `yaml:"password"`
	Timeout  time.Duration `yaml:"timeout"`
}

type rabbitmqCollector struct {
	cfg    rabbitmqConfig
	client *http.Client
}

func init() {
	Register("rabbitmq", func() Collector { return &rabbitmqCollector{} })
}

func (r *rabbitmqCollector) Name() string { return "rabbitmq" }

func (r *rabbitmqCollector) Configure(decode func(v any) error) error {
	if err := decode(&r.cfg); err != nil {
		return err
	}
	if r.cfg.URL == "" {
		r.cfg.URL = "http://127.0.0.1:15672"
	}
	if r.cfg.User == "" {
		r.cfg.User = "guest"
	}
	if r.cfg.Password == "" {
		r.cfg.Password = "guest"
	}
	if r.cfg.Timeout <= 0 {
		r.cfg.Timeout = 2 * time.Second
	}
	return nil
}

func (r *rabbitmqCollector) Init(reg *registry.Registry) error {
	if r.cfg.URL == "" {
		if err := r.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	r.client = &http.Client{Timeout: r.cfg.Timeout}
	if _, err := r.overview(context.Background()); err != nil {
		return err
	}
	inc := registry.Incremental
	for _, c := range []*registry.Chart{
		{ID: "rabbitmq.messages", Title: "RabbitMQ queued messages", Units: "messages", Type: registry.Stacked, Priority: 48000,
			Dimensions: []*registry.Dimension{{ID: "ready"}, {ID: "unacked"}, {ID: "total"}}},
		{ID: "rabbitmq.messages_stats", Title: "RabbitMQ message rates", Units: "messages/s", Priority: 48010,
			Dimensions: []*registry.Dimension{
				{ID: "publish", Algorithm: inc}, {ID: "deliver", Algorithm: inc},
				{ID: "ack", Algorithm: inc}, {ID: "get", Algorithm: inc}}},
		{ID: "rabbitmq.objects", Title: "RabbitMQ objects", Units: "objects", Priority: 48020,
			Dimensions: []*registry.Dimension{{ID: "connections"}, {ID: "channels"}, {ID: "queues"}, {ID: "consumers"}, {ID: "exchanges"}}},
	} {
		c.Family, c.Plugin, c.Module = "rabbitmq", "rabbitmq", "rabbitmq"
		reg.AddChart(c)
	}
	return nil
}

type rmqOverview struct {
	ObjectTotals struct {
		Connections float64 `json:"connections"`
		Channels    float64 `json:"channels"`
		Queues      float64 `json:"queues"`
		Consumers   float64 `json:"consumers"`
		Exchanges   float64 `json:"exchanges"`
	} `json:"object_totals"`
	QueueTotals struct {
		Messages               float64 `json:"messages"`
		MessagesReady          float64 `json:"messages_ready"`
		MessagesUnacknowledged float64 `json:"messages_unacknowledged"`
	} `json:"queue_totals"`
	MessageStats struct {
		Publish    float64 `json:"publish"`
		DeliverGet float64 `json:"deliver_get"`
		Ack        float64 `json:"ack"`
		Get        float64 `json:"get"`
		Deliver    float64 `json:"deliver"`
	} `json:"message_stats"`
}

func (r *rabbitmqCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	o, err := r.overview(ctx)
	if err != nil {
		return err
	}
	_ = reg.Collect("rabbitmq.messages", now, map[string]float64{
		"ready": o.QueueTotals.MessagesReady, "unacked": o.QueueTotals.MessagesUnacknowledged, "total": o.QueueTotals.Messages,
	})
	deliver := o.MessageStats.Deliver
	if deliver == 0 {
		deliver = o.MessageStats.DeliverGet
	}
	_ = reg.Collect("rabbitmq.messages_stats", now, map[string]float64{
		"publish": o.MessageStats.Publish, "deliver": deliver, "ack": o.MessageStats.Ack, "get": o.MessageStats.Get,
	})
	_ = reg.Collect("rabbitmq.objects", now, map[string]float64{
		"connections": o.ObjectTotals.Connections, "channels": o.ObjectTotals.Channels,
		"queues": o.ObjectTotals.Queues, "consumers": o.ObjectTotals.Consumers, "exchanges": o.ObjectTotals.Exchanges,
	})
	return nil
}

func (r *rabbitmqCollector) overview(ctx context.Context) (rmqOverview, error) {
	var o rmqOverview
	url := strings.TrimRight(r.cfg.URL, "/") + "/api/overview"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return o, err
	}
	req.SetBasicAuth(r.cfg.User, r.cfg.Password)
	resp, err := r.client.Do(req)
	if err != nil {
		return o, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return o, err
	}
	if resp.StatusCode >= 300 {
		return o, fmt.Errorf("rabbitmq: HTTP %d", resp.StatusCode)
	}
	if err := json.Unmarshal(body, &o); err != nil {
		return o, err
	}
	return o, nil
}
