package collect

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// hdfsConfig is collectors.modules.hdfs (NameNode JMX).
type hdfsConfig struct {
	URL     string        `yaml:"url"`
	Timeout time.Duration `yaml:"timeout"`
}

type hdfsCollector struct {
	cfg    hdfsConfig
	client *http.Client
	url    string
}

func init() {
	Register("hdfs", func() Collector { return &hdfsCollector{} })
}

func (h *hdfsCollector) Name() string { return "hdfs" }

func (h *hdfsCollector) Configure(decode func(v any) error) error {
	if err := decode(&h.cfg); err != nil {
		return err
	}
	if h.cfg.Timeout <= 0 {
		h.cfg.Timeout = 3 * time.Second
	}
	return nil
}

func (h *hdfsCollector) Init(reg *registry.Registry) error {
	if h.cfg.Timeout <= 0 {
		if err := h.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	h.client = &http.Client{Timeout: h.cfg.Timeout}
	urls := []string{h.cfg.URL}
	if h.cfg.URL == "" {
		urls = []string{
			"http://127.0.0.1:9870/jmx",
			"http://127.0.0.1:50070/jmx",
		}
	}
	u, body, err := httpGetTry(context.Background(), h.client, urls)
	if err != nil {
		return err
	}
	if _, err := parseHDFSJMX(body); err != nil {
		return err
	}
	h.url = u
	for _, c := range []*registry.Chart{
		{ID: "hdfs.capacity", Title: "Capacity Across All Datanodes", Units: "KiB", Type: registry.Stacked, Priority: 51900,
			Dimensions: []*registry.Dimension{{ID: "remaining", Divisor: 1024}, {ID: "used", Divisor: 1024}}},
		{ID: "hdfs.files_total", Title: "Number of Tracked Files", Units: "num", Priority: 51910,
			Dimensions: []*registry.Dimension{{ID: "files"}}},
		{ID: "hdfs.blocks_total", Title: "Number of Allocated Blocks in the System", Units: "num", Priority: 51919,
			Dimensions: []*registry.Dimension{{ID: "blocks"}}},
		{ID: "hdfs.blocks", Title: "Number of Problem Blocks", Units: "num", Priority: 51920,
			Dimensions: []*registry.Dimension{{ID: "corrupt"}, {ID: "missing"}, {ID: "under_replicated"}}},
		{ID: "hdfs.data_nodes", Title: "Number of Data Nodes By Status", Units: "num", Type: registry.Stacked, Priority: 51930,
			Dimensions: []*registry.Dimension{{ID: "live"}, {ID: "dead"}}},
		{ID: "hdfs.load", Title: "Number of Concurrent File Accesses", Units: "load", Priority: 51940,
			Dimensions: []*registry.Dimension{{ID: "load"}}},
	} {
		c.Family, c.Plugin, c.Module = "hdfs", "hdfs", "hdfs"
		reg.AddChart(c)
	}
	return nil
}

func (h *hdfsCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	b, err := httpGet(ctx, h.client, h.url)
	if err != nil {
		return err
	}
	st, err := parseHDFSJMX(b)
	if err != nil {
		return err
	}
	_ = reg.Collect("hdfs.capacity", now, map[string]float64{"used": st.used, "remaining": st.remaining})
	_ = reg.Collect("hdfs.files_total", now, map[string]float64{"files": st.files})
	_ = reg.Collect("hdfs.blocks_total", now, map[string]float64{"blocks": st.blocks})
	_ = reg.Collect("hdfs.blocks", now, map[string]float64{
		"missing": st.missing, "corrupt": st.corrupt, "under_replicated": st.under})
	_ = reg.Collect("hdfs.data_nodes", now, map[string]float64{"live": st.live, "dead": st.dead})
	_ = reg.Collect("hdfs.load", now, map[string]float64{"load": st.load})
	return nil
}

type hdfsStats struct {
	used, remaining, files, blocks, missing, corrupt, under, live, dead, load float64
}

func parseHDFSJMX(b []byte) (hdfsStats, error) {
	var raw struct {
		Beans []map[string]any `json:"beans"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return hdfsStats{}, err
	}
	st := hdfsStats{}
	found := false
	for _, bean := range raw.Beans {
		name, _ := bean["name"].(string)
		if !strings.Contains(name, "NameNode") && !strings.Contains(name, "FSNamesystem") {
			continue
		}
		if v := jsonNum(bean["CapacityUsed"]); v != 0 || bean["CapacityUsed"] != nil {
			st.used = v
			found = true
		}
		if v := jsonNum(bean["CapacityRemaining"]); v != 0 || bean["CapacityRemaining"] != nil {
			st.remaining = v
			found = true
		}
		if v := jsonNum(bean["FilesTotal"]); v != 0 || bean["FilesTotal"] != nil {
			st.files = v
		}
		if v := jsonNum(bean["BlocksTotal"]); v != 0 || bean["BlocksTotal"] != nil {
			st.blocks = v
		}
		st.missing = firstNonZero(st.missing, jsonNum(bean["MissingBlocks"]))
		st.corrupt = firstNonZero(st.corrupt, jsonNum(bean["CorruptBlocks"]))
		st.under = firstNonZero(st.under, jsonNum(bean["UnderReplicatedBlocks"]))
		st.live = firstNonZero(st.live, jsonNum(bean["NumLiveDataNodes"]))
		st.dead = firstNonZero(st.dead, jsonNum(bean["NumDeadDataNodes"]))
		st.load = firstNonZero(st.load, jsonNum(bean["TotalLoad"]))
	}
	if !found {
		return hdfsStats{}, fmt.Errorf("hdfs: no NameNode capacity beans")
	}
	return st, nil
}

func firstNonZero(a, b float64) float64 {
	if a != 0 {
		return a
	}
	return b
}
