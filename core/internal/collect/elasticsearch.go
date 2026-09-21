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

// elasticsearchConfig is collectors.modules.elasticsearch.
type elasticsearchConfig struct {
	URL      string        `yaml:"url"`
	User     string        `yaml:"user"`
	Password string        `yaml:"password"`
	Timeout  time.Duration `yaml:"timeout"`
}

type elasticsearchCollector struct {
	cfg    elasticsearchConfig
	client *http.Client
}

func init() {
	Register("elasticsearch", func() Collector { return &elasticsearchCollector{} })
}

func (e *elasticsearchCollector) Name() string { return "elasticsearch" }

func (e *elasticsearchCollector) Configure(decode func(v any) error) error {
	if err := decode(&e.cfg); err != nil {
		return err
	}
	if e.cfg.URL == "" {
		e.cfg.URL = "http://127.0.0.1:9200"
	}
	if e.cfg.Timeout <= 0 {
		e.cfg.Timeout = 2 * time.Second
	}
	return nil
}

func (e *elasticsearchCollector) Init(reg *registry.Registry) error {
	if e.cfg.URL == "" {
		if err := e.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	e.client = &http.Client{Timeout: e.cfg.Timeout}
	if _, _, err := e.fetch(context.Background()); err != nil {
		return err
	}
	inc := registry.Incremental
	for _, c := range []*registry.Chart{
		{ID: "elasticsearch.cluster_health_status", Title: "Elasticsearch cluster status", Units: "status", Type: registry.Line, Priority: 47000,
			Dimensions: []*registry.Dimension{{ID: "green"}, {ID: "yellow"}, {ID: "red"}}},
		{ID: "elasticsearch.cluster_nodes", Title: "Elasticsearch nodes", Units: "nodes", Priority: 47010,
			Dimensions: []*registry.Dimension{{ID: "nodes"}, {ID: "data"}}},
		{ID: "elasticsearch.cluster_shards", Title: "Elasticsearch shards", Units: "shards", Type: registry.Stacked, Priority: 47020,
			Dimensions: []*registry.Dimension{{ID: "active"}, {ID: "relocating"}, {ID: "initializing"}, {ID: "unassigned"}}},
		{ID: "elasticsearch.node_docs", Title: "Elasticsearch documents", Units: "documents", Priority: 47030,
			Dimensions: []*registry.Dimension{{ID: "count"}}},
		{ID: "elasticsearch.node_store", Title: "Elasticsearch store size", Units: "MiB", Priority: 47040,
			Dimensions: []*registry.Dimension{{ID: "size", Divisor: 1 << 20}}},
		{ID: "elasticsearch.node_indexing", Title: "Elasticsearch indexing", Units: "operations/s", Priority: 47050,
			Dimensions: []*registry.Dimension{{ID: "index", Algorithm: inc}, {ID: "delete", Algorithm: inc}}},
		{ID: "elasticsearch.node_search", Title: "Elasticsearch search", Units: "operations/s", Priority: 47060,
			Dimensions: []*registry.Dimension{{ID: "query", Algorithm: inc}, {ID: "fetch", Algorithm: inc}}},
		{ID: "elasticsearch.node_jvm_mem", Title: "Elasticsearch JVM heap", Units: "MiB", Type: registry.Stacked, Priority: 47070,
			Dimensions: []*registry.Dimension{{ID: "used", Divisor: 1 << 20}, {ID: "committed", Divisor: 1 << 20}}},
		{ID: "elasticsearch.node_jvm_gc", Title: "Elasticsearch JVM GC", Units: "ms/s", Priority: 47080,
			Dimensions: []*registry.Dimension{{ID: "young", Algorithm: inc}, {ID: "old", Algorithm: inc}}},
		{ID: "elasticsearch.node_http", Title: "Elasticsearch HTTP connections", Units: "connections", Priority: 47090,
			Dimensions: []*registry.Dimension{{ID: "current"}}},
	} {
		c.Family, c.Plugin, c.Module = "elasticsearch", "elasticsearch", "elasticsearch"
		reg.AddChart(c)
	}
	return nil
}

type esHealth struct {
	Status             string  `json:"status"`
	NumberOfNodes      float64 `json:"number_of_nodes"`
	NumberOfDataNodes  float64 `json:"number_of_data_nodes"`
	ActiveShards       float64 `json:"active_shards"`
	RelocatingShards   float64 `json:"relocating_shards"`
	InitializingShards float64 `json:"initializing_shards"`
	UnassignedShards   float64 `json:"unassigned_shards"`
}

type esNodes struct {
	Nodes map[string]esNode `json:"nodes"`
}

type esNode struct {
	JVM struct {
		Mem struct {
			HeapUsedInBytes      float64 `json:"heap_used_in_bytes"`
			HeapCommittedInBytes float64 `json:"heap_committed_in_bytes"`
		} `json:"mem"`
		GC struct {
			Collectors map[string]struct {
				CollectionTimeInMillis float64 `json:"collection_time_in_millis"`
			} `json:"collectors"`
		} `json:"gc"`
	} `json:"jvm"`
	Indices struct {
		Docs struct {
			Count float64 `json:"count"`
		} `json:"docs"`
		Store struct {
			SizeInBytes float64 `json:"size_in_bytes"`
		} `json:"store"`
		Indexing struct {
			IndexTotal  float64 `json:"index_total"`
			DeleteTotal float64 `json:"delete_total"`
		} `json:"indexing"`
		Search struct {
			QueryTotal float64 `json:"query_total"`
			FetchTotal float64 `json:"fetch_total"`
		} `json:"search"`
	} `json:"indices"`
	HTTP struct {
		CurrentOpen float64 `json:"current_open"`
	} `json:"http"`
}

func (e *elasticsearchCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	h, n, err := e.fetch(ctx)
	if err != nil {
		return err
	}
	green, yellow, red := 0.0, 0.0, 0.0
	switch strings.ToLower(h.Status) {
	case "green":
		green = 1
	case "yellow":
		yellow = 1
	default:
		red = 1
	}
	_ = reg.Collect("elasticsearch.cluster_health_status", now, map[string]float64{"green": green, "yellow": yellow, "red": red})
	_ = reg.Collect("elasticsearch.cluster_nodes", now, map[string]float64{"nodes": h.NumberOfNodes, "data": h.NumberOfDataNodes})
	_ = reg.Collect("elasticsearch.cluster_shards", now, map[string]float64{
		"active": h.ActiveShards, "relocating": h.RelocatingShards,
		"initializing": h.InitializingShards, "unassigned": h.UnassignedShards,
	})
	var node esNode
	for _, v := range n.Nodes {
		node = v
		break
	}
	_ = reg.Collect("elasticsearch.node_docs", now, map[string]float64{"count": node.Indices.Docs.Count})
	_ = reg.Collect("elasticsearch.node_store", now, map[string]float64{"size": node.Indices.Store.SizeInBytes})
	_ = reg.Collect("elasticsearch.node_indexing", now, map[string]float64{"index": node.Indices.Indexing.IndexTotal, "delete": node.Indices.Indexing.DeleteTotal})
	_ = reg.Collect("elasticsearch.node_search", now, map[string]float64{"query": node.Indices.Search.QueryTotal, "fetch": node.Indices.Search.FetchTotal})
	_ = reg.Collect("elasticsearch.node_jvm_mem", now, map[string]float64{"used": node.JVM.Mem.HeapUsedInBytes, "committed": node.JVM.Mem.HeapCommittedInBytes})
	young := node.JVM.GC.Collectors["young"].CollectionTimeInMillis
	if young == 0 {
		young = node.JVM.GC.Collectors["ParNew"].CollectionTimeInMillis
	}
	old := node.JVM.GC.Collectors["old"].CollectionTimeInMillis
	if old == 0 {
		old = node.JVM.GC.Collectors["ConcurrentMarkSweep"].CollectionTimeInMillis
	}
	_ = reg.Collect("elasticsearch.node_jvm_gc", now, map[string]float64{"young": young, "old": old})
	_ = reg.Collect("elasticsearch.node_http", now, map[string]float64{"current": node.HTTP.CurrentOpen})
	return nil
}

func (e *elasticsearchCollector) fetch(ctx context.Context) (esHealth, esNodes, error) {
	var h esHealth
	var n esNodes
	if err := e.getJSON(ctx, "/_cluster/health", &h); err != nil {
		return h, n, err
	}
	if err := e.getJSON(ctx, "/_nodes/_local/stats", &n); err != nil {
		return h, n, err
	}
	return h, n, nil
}

func (e *elasticsearchCollector) getJSON(ctx context.Context, path string, dest any) error {
	url := strings.TrimRight(e.cfg.URL, "/") + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	if e.cfg.User != "" {
		req.SetBasicAuth(e.cfg.User, e.cfg.Password)
	}
	resp, err := e.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("elasticsearch %s: HTTP %d", path, resp.StatusCode)
	}
	return json.Unmarshal(body, dest)
}
