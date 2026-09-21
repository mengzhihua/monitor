package collect

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

const (
	rethinkVersionV04 uint32 = 0x400c2d20
	rethinkProtoJSON  uint32 = 0x7e6970c7
)

// rethinkdbConfig is collectors.modules.rethinkdb (JSON protocol :28015).
type rethinkdbConfig struct {
	Address string        `yaml:"address"`
	Timeout time.Duration `yaml:"timeout"`
}

type rethinkdbCollector struct {
	cfg   rethinkdbConfig
	dial  func(ctx context.Context, network, address string) (net.Conn, error)
	stats func(ctx context.Context) ([]map[string]any, error)
}

func init() {
	Register("rethinkdb", func() Collector { return &rethinkdbCollector{} })
}

func (r *rethinkdbCollector) Name() string { return "rethinkdb" }

func (r *rethinkdbCollector) Configure(decode func(v any) error) error {
	if err := decode(&r.cfg); err != nil {
		return err
	}
	if r.cfg.Address == "" {
		r.cfg.Address = "127.0.0.1:28015"
	}
	if r.cfg.Timeout <= 0 {
		r.cfg.Timeout = 2 * time.Second
	}
	return nil
}

func (r *rethinkdbCollector) Init(reg *registry.Registry) error {
	if r.cfg.Address == "" {
		if err := r.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	if _, err := r.cluster(context.Background()); err != nil {
		return err
	}
	inc := registry.Incremental
	for _, ch := range []*registry.Chart{
		{ID: "rethinkdb.cluster_client_connections", Title: "Cluster Client Connections", Units: "connections", Priority: 59300,
			Dimensions: []*registry.Dimension{{ID: "connections"}}},
		{ID: "rethinkdb.cluster_active_clients", Title: "Cluster Active Clients", Units: "clients", Priority: 59310,
			Dimensions: []*registry.Dimension{{ID: "active"}}},
		{ID: "rethinkdb.cluster_queries", Title: "Cluster Queries", Units: "queries/s", Priority: 59320,
			Dimensions: []*registry.Dimension{{ID: "queries", Algorithm: inc}}},
		{ID: "rethinkdb.cluster_documents", Title: "Cluster Documents", Units: "documents/s", Priority: 59330,
			Dimensions: []*registry.Dimension{{ID: "read", Algorithm: inc}, {ID: "written", Algorithm: inc, Multiplier: -1}}},
		{ID: "rethinkdb.cluster_servers_stats_request", Title: "Cluster Servers Stats Request", Units: "servers", Priority: 59340,
			Dimensions: []*registry.Dimension{{ID: "success"}, {ID: "timeout"}}},
	} {
		ch.Family, ch.Plugin, ch.Module = "rethinkdb", "rethinkdb", "rethinkdb"
		reg.AddChart(ch)
	}
	return nil
}

func (r *rethinkdbCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	st, err := r.cluster(ctx)
	if err != nil {
		return err
	}
	_ = reg.Collect("rethinkdb.cluster_client_connections", now, map[string]float64{"connections": st.conn})
	_ = reg.Collect("rethinkdb.cluster_active_clients", now, map[string]float64{"active": st.active})
	_ = reg.Collect("rethinkdb.cluster_queries", now, map[string]float64{"queries": st.queries})
	_ = reg.Collect("rethinkdb.cluster_documents", now, map[string]float64{"read": st.read, "written": st.written})
	_ = reg.Collect("rethinkdb.cluster_servers_stats_request", now, map[string]float64{"success": st.ok, "timeout": st.timeout})
	return nil
}

type rethinkCluster struct{ conn, active, queries, read, written, ok, timeout float64 }

func (r *rethinkdbCollector) cluster(ctx context.Context) (rethinkCluster, error) {
	fn := r.stats
	if fn == nil {
		fn = r.statsTCP
	}
	items, err := fn(ctx)
	if err != nil {
		return rethinkCluster{}, err
	}
	var st rethinkCluster
	for _, m := range items {
		id0 := ""
		if ids := nestSlice(m, "id"); len(ids) > 0 {
			id0, _ = ids[0].(string)
		}
		if id0 == "cluster" {
			continue
		}
		if nestString(m, "error") != "" {
			st.timeout++
			continue
		}
		st.ok++
		qe := nestMap(m, "query_engine")
		st.conn += nestFloat(qe, "client_connections")
		st.active += nestFloat(qe, "clients_active")
		st.queries += nestFloat(qe, "queries_total")
		st.read += nestFloat(qe, "read_docs_total")
		st.written += nestFloat(qe, "written_docs_total")
	}
	return st, nil
}

func (r *rethinkdbCollector) statsTCP(ctx context.Context) ([]map[string]any, error) {
	dial := r.dial
	if dial == nil {
		d := net.Dialer{Timeout: r.cfg.Timeout}
		dial = d.DialContext
	}
	conn, err := dial(ctx, "tcp", r.cfg.Address)
	if err != nil {
		return nil, fmt.Errorf("rethinkdb: %w", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(r.cfg.Timeout))
	var hdr [12]byte
	binary.LittleEndian.PutUint32(hdr[0:4], rethinkVersionV04)
	binary.LittleEndian.PutUint32(hdr[4:8], 0)
	binary.LittleEndian.PutUint32(hdr[8:12], rethinkProtoJSON)
	if _, err := conn.Write(hdr[:]); err != nil {
		return nil, err
	}
	resp, err := bufio.NewReader(conn).ReadString(0)
	if err != nil {
		return nil, fmt.Errorf("rethinkdb: handshake: %w", err)
	}
	if !strings.Contains(strings.ToUpper(resp), "SUCCESS") {
		return nil, fmt.Errorf("rethinkdb: handshake %q", strings.TrimSpace(resp))
	}
	q := `[1,[15,[[14,["rethinkdb"]],"stats"]],{}]`
	var qh [12]byte
	binary.LittleEndian.PutUint64(qh[0:8], 1)
	binary.LittleEndian.PutUint32(qh[8:12], uint32(len(q)))
	if _, err := conn.Write(qh[:]); err != nil {
		return nil, err
	}
	if _, err := io.WriteString(conn, q); err != nil {
		return nil, err
	}
	var rh [12]byte
	if _, err := io.ReadFull(conn, rh[:]); err != nil {
		return nil, fmt.Errorf("rethinkdb: %w", err)
	}
	n := binary.LittleEndian.Uint32(rh[8:12])
	if n == 0 || n > 8<<20 {
		return nil, fmt.Errorf("rethinkdb: bad length")
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(conn, body); err != nil {
		return nil, err
	}
	var wrap []any
	if err := json.Unmarshal(body, &wrap); err != nil {
		return nil, fmt.Errorf("rethinkdb: %w", err)
	}
	if len(wrap) < 2 {
		return nil, fmt.Errorf("rethinkdb: empty stats")
	}
	raw, _ := wrap[1].([]any)
	var out []map[string]any
	for _, v := range raw {
		if m, ok := v.(map[string]any); ok {
			out = append(out, m)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("rethinkdb: no servers")
	}
	return out, nil
}
