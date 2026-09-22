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
		{ID: "mq.queue_managers", Context: "mq.queue_managers", Title: "IBM MQ queue managers", Units: "managers", Family: "mq", Type: registry.Stacked, Priority: 64300,
			Dimensions: []*registry.Dimension{{ID: "running"}, {ID: "stopped"}}},
		{ID: "mq.queues", Context: "mq.queues", Title: "IBM MQ local queues", Units: "queues", Family: "mq", Priority: 64310,
			Dimensions: []*registry.Dimension{{ID: "local"}}},
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
	states := map[string]float64{"running": 0, "stopped": 0}
	for _, q := range qms {
		if strings.EqualFold(q.Status, "Running") {
			states["running"]++
		} else {
			states["stopped"]++
		}
	}
	_ = reg.Collect("mq.queue_managers", now, states)

	qm := m.cfg.QueueManager
	if qm == "" && len(qms) > 0 {
		qm = qms[0].Name
	}
	queues, err := m.queueStatus(ctx, qm)
	if err != nil {
		return err
	}
	_ = reg.Collect("mq.queues", now, map[string]float64{"local": float64(len(queues))})
	for _, q := range queues {
		id := "mq.queue_depth." + sanitizeID(q.Name)
		if !m.seen[id] {
			m.seen[id] = true
			ch := sysChart(id, "mq", "IBM MQ queue depth "+q.Name, "messages", 64320, &registry.Dimension{ID: "curdepth"})
			ch.Context, ch.Plugin, ch.Module = "mq.queue_depth", "ibm.d", "mq"
			reg.AddChart(ch)
		}
		_ = reg.Collect(id, now, map[string]float64{"curdepth": q.Depth})
	}
	return nil
}

type mqManager struct {
	Name, Status string
}

type mqQueue struct {
	Name  string
	Depth float64
}

var (
	reDspmq   = regexp.MustCompile(`QMNAME\(([^)]+)\)\s+STATUS\(([^)]+)\)`)
	reMQQueue = regexp.MustCompile(`QUEUE\(([^)]+)\)`)
	reMQDepth = regexp.MustCompile(`CURDEPTH\((\d+)\)`)
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
	blocks := strings.Split(string(b), "QUEUE(")
	var out []mqQueue
	for _, blk := range blocks[1:] {
		nameM := reMQQueue.FindStringSubmatch("QUEUE(" + blk)
		depthM := reMQDepth.FindStringSubmatch(blk)
		if len(nameM) != 2 {
			continue
		}
		q := mqQueue{Name: nameM[1]}
		if len(depthM) == 2 {
			q.Depth = firstFloat(depthM[1])
		}
		out = append(out, q)
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

func (m *mqCollector) queueStatus(ctx context.Context, qm string) ([]mqQueue, error) {
	run := m.run
	if run == nil {
		run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
			cctx, cancel := context.WithTimeout(ctx, m.cfg.Timeout)
			defer cancel()
			cmd := exec.CommandContext(cctx, name, args...)
			cmd.Stdin = strings.NewReader("DISPLAY QSTATUS(*) CURDEPTH\n")
			return cmd.Output()
		}
	}
	b, err := run(ctx, m.cfg.Runmqsc, qm)
	if err != nil {
		return nil, nil
	}
	return parseRunmqscQueues(b), nil
}
