package collect

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// mqConfig is collectors.modules.mq (ibm.d IBM MQ via dspmq/runmqsc, no CGO).
type mqConfig struct {
	QueueManager string        `yaml:"queue_manager"`
	Command      string        `yaml:"command"` // dspmq
	Runmqsc      string        `yaml:"runmqsc"`
	Timeout      time.Duration `yaml:"timeout"`
}

type mqCollector struct {
	cfg  mqConfig
	run  func(ctx context.Context, name string, args ...string) ([]byte, error)
	seen map[string]bool
}

func init() {
	Register("mq", func() Collector { return &mqCollector{} })
}

func (m *mqCollector) Name() string { return "mq" }

func (m *mqCollector) Configure(decode func(v any) error) error {
	if err := decode(&m.cfg); err != nil {
		return err
	}
	if m.cfg.Command == "" {
		m.cfg.Command = "dspmq"
	}
	if m.cfg.Runmqsc == "" {
		m.cfg.Runmqsc = "runmqsc"
	}
	if m.cfg.Timeout <= 0 {
		m.cfg.Timeout = 5 * time.Second
	}
	return nil
}

func (m *mqCollector) Init(reg *registry.Registry) error {
	if m.cfg.Command == "" {
		if err := m.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	qms, err := m.managers(context.Background())
	if err != nil {
		return err
	}
	if len(qms) == 0 {
		return fmt.Errorf("mq: no queue managers")
	}
	m.seen = map[string]bool{}
	for _, ch := range []*registry.Chart{
		{ID: "mq.qmgr.status", Context: "mq.qmgr.status", Title: "Queue Manager Status", Units: "status", Family: "mq", Priority: 64300,
			Dimensions: []*registry.Dimension{{ID: "status"}}},
		{ID: "mq.queues.overview", Context: "mq.queues.overview", Title: "Queues Monitoring Status", Units: "queues", Family: "mq", Priority: 64310,
			Dimensions: []*registry.Dimension{{ID: "monitored"}}},
	} {
		ch.Plugin, ch.Module = "ibm.d", "mq"
		reg.AddChart(ch)
	}
	return nil
}

func (m *mqCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	qms, err := m.managers(ctx)
	if err != nil {
		return err
	}
	selected := m.cfg.QueueManager
	if selected == "" && len(qms) == 1 {
		selected = qms[0].Name
	}
	for _, q := range qms {
		st := 0.0
		if strings.EqualFold(q.Status, "Running") {
			st = 1
		}
		if selected != "" && q.Name == selected {
			_ = reg.Collect("mq.qmgr.status", now, map[string]float64{"status": st})
		}
		if len(qms) > 1 {
			id := "mq.qmgr.status." + sanitizeID(q.Name)
			if !m.seen[id] {
				m.seen[id] = true
				ch := sysChart(id, "mq", "Queue Manager Status "+q.Name, "status", 64300, &registry.Dimension{ID: "status"})
				ch.Context, ch.Plugin, ch.Module = "mq.qmgr.status", "ibm.d", "mq"
				reg.AddChart(ch)
			}
			_ = reg.Collect(id, now, map[string]float64{"status": st})
		}
	}

	qm := selected
	if qm == "" && len(qms) > 0 {
		qm = qms[0].Name
	}
	queues, err := m.queueStatus(ctx, qm)
	if err != nil {
		return err
	}
	_ = reg.Collect("mq.queues.overview", now, map[string]float64{"monitored": float64(len(queues))})
	for _, q := range queues {
		sid := sanitizeID(q.Name)
		m.ensureQueueCharts(reg, q.Name, sid)
		pct := 0.0
		if q.MaxDepth > 0 {
			pct = q.Depth * 100 / q.MaxDepth
		}
		_ = reg.Collect("mq.queue.depth."+sid, now, map[string]float64{"current": q.Depth, "max": q.MaxDepth})
		_ = reg.Collect("mq.queue.depth_percentage."+sid, now, map[string]float64{"percentage": pct})
		_ = reg.Collect("mq.queue.messages."+sid, now, map[string]float64{"enqueued": q.MsgIn, "dequeued": q.MsgOut})
		_ = reg.Collect("mq.queue.connections."+sid, now, map[string]float64{"input": q.IPProcs, "output": q.OPProcs})
	}
	return nil
}

func (m *mqCollector) ensureQueueCharts(reg *registry.Registry, name, sid string) {
	depthID := "mq.queue.depth." + sid
	if m.seen[depthID] {
		return
	}
	m.seen[depthID] = true
	add := func(id, ctx, title, units string, prio int, dims ...*registry.Dimension) {
		ch := sysChart(id, "mq", title+" "+name, units, prio, dims...)
		ch.Context, ch.Plugin, ch.Module = ctx, "ibm.d", "mq"
		reg.AddChart(ch)
	}
	add(depthID, "mq.queue.depth", "Queue Depth", "messages", 64320,
		&registry.Dimension{ID: "current"}, &registry.Dimension{ID: "max"})
	add("mq.queue.depth_percentage."+sid, "mq.queue.depth_percentage", "Queue Depth Percentage", "percentage", 64321,
		&registry.Dimension{ID: "percentage"})
	add("mq.queue.messages."+sid, "mq.queue.messages", "Queue Messages", "messages/s", 64322,
		incDim("enqueued"), incDim("dequeued"))
	add("mq.queue.connections."+sid, "mq.queue.connections", "Queue Connections", "connections", 64323,
		&registry.Dimension{ID: "input"}, &registry.Dimension{ID: "output"})
}

type mqManager struct {
	Name, Status string
}

type mqQueue struct {
	Name     string
	Depth    float64
	MaxDepth float64
	IPProcs  float64
	OPProcs  float64
	MsgIn    float64
	MsgOut   float64
}

var (
	reDspmq   = regexp.MustCompile(`QMNAME\(([^)]+)\)\s+STATUS\(([^)]+)\)`)
	reMQQueue = regexp.MustCompile(`QUEUE\(([^)]+)\)`)
	reMQDepth = regexp.MustCompile(`CURDEPTH\((\d+)\)`)
	reMQMax   = regexp.MustCompile(`MAXDEPTH\((\d+)\)`)
	reMQIP    = regexp.MustCompile(`IPPROCS\((\d+)\)`)
	reMQOP    = regexp.MustCompile(`OPPROCS\((\d+)\)`)
	reMQIn    = regexp.MustCompile(`MSGIN\((\d+)\)`)
	reMQOut   = regexp.MustCompile(`MSGOUT\((\d+)\)`)
)

func parseDspmq(b []byte) []mqManager {
	var out []mqManager
	for _, line := range strings.Split(string(b), "\n") {
		m := reDspmq.FindStringSubmatch(line)
		if len(m) == 3 {
			out = append(out, mqManager{Name: m[1], Status: m[2]})
		}
	}
	return out
}

func parseRunmqscQueues(b []byte) []mqQueue {
	byName := map[string]*mqQueue{}
	var order []string
	blocks := strings.Split(string(b), "QUEUE(")
	for _, blk := range blocks[1:] {
		nameM := reMQQueue.FindStringSubmatch("QUEUE(" + blk)
		if len(nameM) != 2 {
			continue
		}
		name := nameM[1]
		q, ok := byName[name]
		if !ok {
			q = &mqQueue{Name: name}
			byName[name] = q
			order = append(order, name)
		}
		if m := reMQDepth.FindStringSubmatch(blk); len(m) == 2 {
			q.Depth = firstFloat(m[1])
		}
		if m := reMQMax.FindStringSubmatch(blk); len(m) == 2 {
			q.MaxDepth = firstFloat(m[1])
		}
		if m := reMQIP.FindStringSubmatch(blk); len(m) == 2 {
			q.IPProcs = firstFloat(m[1])
		}
		if m := reMQOP.FindStringSubmatch(blk); len(m) == 2 {
			q.OPProcs = firstFloat(m[1])
		}
		if m := reMQIn.FindStringSubmatch(blk); len(m) == 2 {
			q.MsgIn = firstFloat(m[1])
		}
		if m := reMQOut.FindStringSubmatch(blk); len(m) == 2 {
			q.MsgOut = firstFloat(m[1])
		}
	}
	out := make([]mqQueue, len(order))
	for i, n := range order {
		out[i] = *byName[n]
	}
	return out
}

func (m *mqCollector) managers(ctx context.Context) ([]mqManager, error) {
	run := m.run
	if run == nil {
		run = execRun(m.cfg.Timeout)
	}
	b, err := run(ctx, m.cfg.Command)
	if err != nil {
		return nil, fmt.Errorf("mq: %w", err)
	}
	qms := parseDspmq(b)
	if len(qms) == 0 {
		return nil, fmt.Errorf("mq: no queue managers")
	}
	if m.cfg.QueueManager != "" {
		for _, q := range qms {
			if q.Name == m.cfg.QueueManager {
				return []mqManager{q}, nil
			}
		}
		return nil, fmt.Errorf("mq: queue manager %s not found", m.cfg.QueueManager)
	}
	return qms, nil
}

// QSTATUS accepts CURDEPTH/IPPROCS/OPPROCS; MSGIN/MSGOUT are RESET QSTATS
// (or PCF) and must not be used as QSTATUS selectors or runmqsc fails.
const mqRunmqsc = "DISPLAY QSTATUS(*) CURDEPTH IPPROCS OPPROCS\nDISPLAY QUEUE(*) MAXDEPTH\n"

func (m *mqCollector) queueStatus(ctx context.Context, qm string) ([]mqQueue, error) {
	run := m.run
	if run == nil {
		run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
			cctx, cancel := context.WithTimeout(ctx, m.cfg.Timeout)
			defer cancel()
			cmd := exec.CommandContext(cctx, name, args...)
			cmd.Stdin = strings.NewReader(mqRunmqsc)
			return cmd.Output()
		}
	}
	b, err := run(ctx, m.cfg.Runmqsc, qm)
	if err != nil {
		return nil, nil
	}
	return parseRunmqscQueues(b), nil
}
