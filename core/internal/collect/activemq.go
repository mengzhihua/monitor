package collect

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// activemqConfig is collectors.modules.activemq (web console XML).
type activemqConfig struct {
	URL      string        `yaml:"url"`
	Webadmin string        `yaml:"webadmin"`
	User     string        `yaml:"user"`
	Password string        `yaml:"password"`
	Timeout  time.Duration `yaml:"timeout"`
}

type activemqCollector struct {
	cfg    activemqConfig
	client *http.Client
	url    string
	seen   map[string]bool
}

func init() {
	Register("activemq", func() Collector { return &activemqCollector{} })
}

func (a *activemqCollector) Name() string { return "activemq" }

func (a *activemqCollector) Configure(decode func(v any) error) error {
	if err := decode(&a.cfg); err != nil {
		return err
	}
	if a.cfg.Timeout <= 0 {
		a.cfg.Timeout = 3 * time.Second
	}
	if a.cfg.Webadmin == "" {
		a.cfg.Webadmin = "admin"
	}
	return nil
}

func (a *activemqCollector) Init(reg *registry.Registry) error {
	if a.cfg.Timeout <= 0 {
		if err := a.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	a.client = &http.Client{Timeout: a.cfg.Timeout}
	a.seen = map[string]bool{}
	base := strings.TrimRight(a.cfg.URL, "/")
	if base == "" {
		base = "http://127.0.0.1:8161"
	}
	a.url = base
	if _, err := a.queues(context.Background()); err != nil {
		return err
	}
	return nil
}

func (a *activemqCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	qs, err := a.queues(ctx)
	if err != nil {
		return err
	}
	inc := registry.Incremental
	for _, q := range qs {
		if strings.Contains(q.Name, "Advisory") {
			continue
		}
		id := sanitizeID(q.Name)
		if !a.seen[id] {
			a.seen[id] = true
			for _, ch := range []*registry.Chart{
				{ID: "activemq.messages." + id, Context: "activemq.messages", Title: q.Name + " Messages", Units: "messages/s", Priority: 57600,
					Dimensions: []*registry.Dimension{{ID: "enqueued", Algorithm: inc}, {ID: "dequeued", Algorithm: inc}}},
				{ID: "activemq.unprocessed_messages." + id, Context: "activemq.unprocessed_messages", Title: q.Name + " Unprocessed Messages", Units: "messages", Priority: 57610,
					Dimensions: []*registry.Dimension{{ID: "unprocessed"}}},
				{ID: "activemq.consumers." + id, Context: "activemq.consumers", Title: q.Name + " Consumers", Units: "consumers", Priority: 57620,
					Dimensions: []*registry.Dimension{{ID: "consumers"}}},
			} {
				ch.Family, ch.Plugin, ch.Module = "activemq", "activemq", "activemq"
				reg.AddChart(ch)
			}
		}
		_ = reg.Collect("activemq.messages."+id, now, map[string]float64{"enqueued": float64(q.Stats.EnqueueCount), "dequeued": float64(q.Stats.DequeueCount)})
		_ = reg.Collect("activemq.unprocessed_messages."+id, now, map[string]float64{"unprocessed": float64(q.Stats.EnqueueCount - q.Stats.DequeueCount)})
		_ = reg.Collect("activemq.consumers."+id, now, map[string]float64{"consumers": float64(q.Stats.ConsumerCount)})
	}
	return nil
}

type amqQueues struct {
	Items []amqQueue `xml:"queue"`
}
type amqQueue struct {
	Name  string   `xml:"name,attr"`
	Stats amqStats `xml:"stats"`
}
type amqStats struct {
	ConsumerCount int64 `xml:"consumerCount,attr"`
	EnqueueCount  int64 `xml:"enqueueCount,attr"`
	DequeueCount  int64 `xml:"dequeueCount,attr"`
}

func (a *activemqCollector) queues(ctx context.Context) ([]amqQueue, error) {
	u := a.url + "/" + a.cfg.Webadmin + "/xml/queues.jsp"
	b, err := httpGetAuth(ctx, a.client, u, a.cfg.User, a.cfg.Password)
	if err != nil {
		return nil, fmt.Errorf("activemq: %w", err)
	}
	var qs amqQueues
	if err := xml.Unmarshal(b, &qs); err != nil {
		return nil, fmt.Errorf("activemq: %w", err)
	}
	return qs.Items, nil
}
