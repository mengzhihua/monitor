package collect

import (
	"context"
	"fmt"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	psnet "github.com/shirou/gopsutil/v4/net"
	"github.com/shirou/gopsutil/v4/process"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// netstatCollector exposes the live `network-connections` function (Netdata's
// apps.plugin equivalent) and a summary chart of TCP states.
type netstatCollector struct {
	top             int
	readConnections func(context.Context, string) ([]psnet.ConnectionStat, error)
	readProcess     func(context.Context, int32) (string, string)
}

type netstatConfig struct {
	Top int `yaml:"top"` // max rows from the function (default 500)
}

func init() {
	Register("netstat", func() Collector { return &netstatCollector{} })
}

func (n *netstatCollector) Name() string { return "netstat" }

func (n *netstatCollector) Configure(decode func(v any) error) error {
	var cfg netstatConfig
	if err := decode(&cfg); err != nil {
		return err
	}
	n.top = cfg.Top
	if n.top <= 0 {
		n.top = 500
	}
	return nil
}

var tcpStates = []string{"ESTABLISHED", "SYN_SENT", "SYN_RECV", "FIN_WAIT1", "FIN_WAIT2",
	"TIME_WAIT", "CLOSE", "CLOSE_WAIT", "LAST_ACK", "LISTEN", "CLOSING"}

func (n *netstatCollector) Init(reg *registry.Registry) error {
	if n.top <= 0 {
		if err := n.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	if _, err := n.read(context.Background(), "tcp"); err != nil {
		return err
	}
	ch := &registry.Chart{ID: "ip.tcpsock", Family: "ip", Title: "TCP sockets by state", Units: "connections",
		Type: registry.Stacked, Priority: 3500, Plugin: "netstat", Module: "netstat"}
	for _, st := range tcpStates {
		ch.Dimensions = append(ch.Dimensions, &registry.Dimension{ID: strings.ToLower(st)})
	}
	reg.AddChart(ch)
	reg.AddChart(&registry.Chart{ID: "ip.sockstat", Family: "ip", Title: "Sockets in use", Units: "sockets",
		Priority: 3501, Plugin: "netstat", Module: "netstat",
		Dimensions: []*registry.Dimension{{ID: "tcp"}, {ID: "udp"}, {ID: "unix"}}})
	return nil
}

func (n *netstatCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	return n.collectSockets(ctx, reg, now, runtime.GOOS == "darwin" || runtime.GOOS == "freebsd")
}

func (n *netstatCollector) read(ctx context.Context, kind string) ([]psnet.ConnectionStat, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var rows []psnet.ConnectionStat
	var err error
	if n.readConnections != nil {
		rows, err = n.readConnections(ctx, kind)
	} else {
		rows, err = psnet.ConnectionsWithContext(ctx, kind)
	}
	// Some platform readers suppress a killed command's error. A deadline
	// still means unavailable data, even if they returned an empty slice.
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return rows, err
}

func (n *netstatCollector) collectSockets(ctx context.Context, reg *registry.Registry, now time.Time, combinedInternet bool) error {
	kind := "tcp"
	if combinedInternet {
		// Darwin/FreeBSD use lsof. One Internet scan contains both TCP and UDP;
		// "all" still excludes Unix sockets, so keep that separate scan.
		kind = "inet"
	}
	connections, err := n.read(ctx, kind)
	if err != nil {
		return err
	}
	tcpCount, udpCount := 0, 0
	states := map[string]float64{}
	for _, s := range tcpStates {
		states[strings.ToLower(s)] = 0
	}
	for _, c := range connections {
		if combinedInternet {
			if c.Family == 1 { // AF_UNIX, including SOCK_STREAM, is not TCP.
				continue
			}
			if c.Type == 2 {
				udpCount++
				continue
			}
			if c.Type != 1 {
				continue
			}
		}
		tcpCount++
		id := strings.ToLower(c.Status)
		if id == "" {
			id = "close"
		}
		states[id]++
	}
	_ = reg.Collect("ip.tcpsock", now, states)

	counts := map[string]float64{"tcp": float64(tcpCount)}
	if combinedInternet {
		counts["udp"] = float64(udpCount)
	} else if udp, err := n.read(ctx, "udp"); err == nil {
		counts["udp"] = float64(len(udp))
	}
	if unixc, err := n.read(ctx, "unix"); err == nil {
		counts["unix"] = float64(len(unixc))
	}
	// Unsupported or failed secondary sources remain missing, not healthy zero.
	_ = reg.Collect("ip.sockstat", now, counts)
	return nil
}

func (n *netstatCollector) Functions() []Function {
	return []Function{{
		Name:    "network-connections",
		Help:    "Live TCP/UDP sockets (local/remote, state, owning process)",
		Timeout: 10,
		Run: func(ctx context.Context, args map[string]string) (any, error) {
			return n.connections(ctx, args)
		},
	}}
}

// ConnRow is one line of the network-connections function.
// Columns follow Netdata network-viewer: protocol, addresses, state, pid,
// process name, cmdline, inode. Lookup failures leave name/cmdline/inode empty.
type ConnRow struct {
	Protocol string `json:"protocol"`
	State    string `json:"state"`
	Local    string `json:"local"`
	Remote   string `json:"remote"`
	PID      int32  `json:"pid"`
	Name     string `json:"name"`
	Cmdline  string `json:"cmdline"`
	Inode    string `json:"inode"`
}

func (n *netstatCollector) connections(ctx context.Context, args map[string]string) (Table, error) {
	kind := args["protocol"]
	if kind == "" {
		kind = args["kind"]
	}
	if kind == "" {
		kind = "all"
	}
	conns, err := n.read(ctx, kind)
	if err != nil && kind == "all" {
		conns, err = n.read(ctx, "inet")
	}
	if err != nil {
		return Table{}, err
	}
	wantState := strings.ToUpper(args["state"])
	wantProto := strings.ToLower(args["protocol"])
	rows := make([]ConnRow, 0, len(conns))
	for _, c := range conns {
		proto := connProto(c.Type)
		if wantProto != "" && wantProto != "all" && wantProto != "inet" && proto != wantProto {
			continue
		}
		if wantState != "" && !strings.EqualFold(c.Status, wantState) {
			continue
		}
		local, remote := fmtAddr(c.Laddr), fmtAddr(c.Raddr)
		rows = append(rows, ConnRow{
			Protocol: proto, State: c.Status,
			Local: local, Remote: remote,
			PID: c.Pid,
		})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].State != rows[j].State {
			return rows[i].State < rows[j].State
		}
		if rows[i].PID != rows[j].PID {
			return rows[i].PID < rows[j].PID
		}
		return rows[i].Local < rows[j].Local
	})
	total := len(rows)
	if len(rows) > n.top {
		rows = rows[:n.top]
	}
	// Resolve expensive process details only for rows that will be returned.
	// Cache failures as well: an inaccessible PID can own many sockets.
	type processDetails struct{ name, cmd string }
	details := map[int32]processDetails{}
	var inodes map[string]string
	if len(rows) > 0 {
		inodes = procNetInodes(nil)
	}
	for i := range rows {
		r := &rows[i]
		if r.PID > 0 {
			d, ok := details[r.PID]
			if !ok {
				if n.readProcess != nil {
					d.name, d.cmd = n.readProcess(ctx, r.PID)
				} else if p, err := process.NewProcessWithContext(ctx, r.PID); err == nil {
					d.name, _ = p.NameWithContext(ctx)
					d.cmd, _ = p.CmdlineWithContext(ctx)
				}
				if len(d.cmd) > 200 {
					d.cmd = d.cmd[:200]
				}
				details[r.PID] = d
			}
			r.Name, r.Cmdline = d.name, d.cmd
		}
		r.Inode = inodes[r.Local+"|"+r.Remote]
	}
	out := Table{Columns: []string{"protocol", "state", "local", "remote", "pid", "name", "cmdline", "inode"}, Total: total}
	out.Rows = make([]any, len(rows))
	for i, r := range rows {
		out.Rows[i] = r
	}
	return out, nil
}

func connProto(t uint32) string {
	switch t {
	case 1: // SOCK_STREAM
		return "tcp"
	case 2: // SOCK_DGRAM
		return "udp"
	}
	return strconv.Itoa(int(t))
}

func fmtAddr(a psnet.Addr) string {
	if a.IP == "" && a.Port == 0 {
		return ""
	}
	if strings.Contains(a.IP, ":") {
		return fmt.Sprintf("[%s]:%d", a.IP, a.Port)
	}
	return fmt.Sprintf("%s:%d", a.IP, a.Port)
}
