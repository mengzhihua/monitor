package collect

import (
	"bufio"
	"bytes"
	"context"
	"crypto/md5"
	"encoding/xml"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// boincConfig is collectors.modules.boinc (GUI RPC :31416).
type boincConfig struct {
	Address  string        `yaml:"address"`
	Password string        `yaml:"password"`
	Timeout  time.Duration `yaml:"timeout"`
}

type boincResult struct {
	State          int
	Active         bool
	ActiveState    int
	SchedulerState int
}

type boincCollector struct {
	cfg     boincConfig
	dial    func(ctx context.Context, network, address string) (net.Conn, error)
	results func(ctx context.Context) ([]boincResult, error)
}

func init() {
	Register("boinc", func() Collector { return &boincCollector{} })
}

func (b *boincCollector) Name() string { return "boinc" }

func (b *boincCollector) Configure(decode func(v any) error) error {
	if err := decode(&b.cfg); err != nil {
		return err
	}
	if b.cfg.Address == "" {
		b.cfg.Address = "127.0.0.1:31416"
	}
	if b.cfg.Timeout <= 0 {
		b.cfg.Timeout = 2 * time.Second
	}
	return nil
}

func (b *boincCollector) Init(reg *registry.Registry) error {
	if b.cfg.Address == "" {
		if err := b.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	if _, err := b.fetch(context.Background()); err != nil {
		return err
	}
	for _, ch := range []*registry.Chart{
		{ID: "boinc.tasks", Title: "Overall Tasks", Units: "tasks", Priority: 60500,
			Dimensions: []*registry.Dimension{{ID: "total"}, {ID: "active"}}},
		{ID: "boinc.tasks_per_state", Title: "Tasks per State", Units: "tasks", Type: registry.Stacked, Priority: 60510,
			Dimensions: []*registry.Dimension{
				{ID: "new"}, {ID: "files_downloading", Name: "downloading"}, {ID: "files_downloaded", Name: "downloaded"},
				{ID: "compute_error"}, {ID: "files_uploading", Name: "uploading"}, {ID: "files_uploaded", Name: "uploaded"},
				{ID: "aborted"}, {ID: "upload_failed"},
			}},
		{ID: "boinc.active_tasks_per_state", Title: "Active Tasks per State", Units: "tasks", Type: registry.Stacked, Priority: 60520,
			Dimensions: []*registry.Dimension{
				{ID: "uninitialized"}, {ID: "executing"}, {ID: "abort_pending"},
				{ID: "quit_pending"}, {ID: "suspended"}, {ID: "copy_pending"},
			}},
		{ID: "boinc.active_tasks_per_scheduler_state", Title: "Active Tasks per Scheduler State", Units: "tasks", Type: registry.Stacked, Priority: 60530,
			Dimensions: []*registry.Dimension{{ID: "uninitialized"}, {ID: "preempted"}, {ID: "scheduled"}}},
	} {
		ch.Family, ch.Plugin, ch.Module = "boinc", "boinc", "boinc"
		reg.AddChart(ch)
	}
	return nil
}

func (b *boincCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	rs, err := b.fetch(ctx)
	if err != nil {
		return err
	}
	mx := map[string]float64{
		"new": 0, "files_downloading": 0, "files_downloaded": 0, "compute_error": 0,
		"files_uploading": 0, "files_uploaded": 0, "aborted": 0, "upload_failed": 0,
		"uninitialized": 0, "executing": 0, "abort_pending": 0, "quit_pending": 0, "suspended": 0, "copy_pending": 0,
		"sched_uninitialized": 0, "preempted": 0, "scheduled": 0,
	}
	var active float64
	for _, r := range rs {
		mx[boincResultState(r.State)]++
		if r.Active {
			active++
			mx[boincActiveState(r.ActiveState)]++
			switch r.SchedulerState {
			case 1:
				mx["preempted"]++
			case 2:
				mx["scheduled"]++
			default:
				mx["sched_uninitialized"]++
			}
		}
	}
	_ = reg.Collect("boinc.tasks", now, map[string]float64{"total": float64(len(rs)), "active": active})
	_ = reg.Collect("boinc.tasks_per_state", now, map[string]float64{
		"new": mx["new"], "files_downloading": mx["files_downloading"], "files_downloaded": mx["files_downloaded"],
		"compute_error": mx["compute_error"], "files_uploading": mx["files_uploading"], "files_uploaded": mx["files_uploaded"],
		"aborted": mx["aborted"], "upload_failed": mx["upload_failed"],
	})
	_ = reg.Collect("boinc.active_tasks_per_state", now, map[string]float64{
		"uninitialized": mx["uninitialized"], "executing": mx["executing"], "abort_pending": mx["abort_pending"],
		"quit_pending": mx["quit_pending"], "suspended": mx["suspended"], "copy_pending": mx["copy_pending"],
	})
	_ = reg.Collect("boinc.active_tasks_per_scheduler_state", now, map[string]float64{
		"uninitialized": mx["sched_uninitialized"], "preempted": mx["preempted"], "scheduled": mx["scheduled"],
	})
	return nil
}

func (b *boincCollector) fetch(ctx context.Context) ([]boincResult, error) {
	if b.results != nil {
		return b.results(ctx)
	}
	return b.rpc(ctx)
}

func (b *boincCollector) rpc(ctx context.Context) ([]boincResult, error) {
	dial := b.dial
	if dial == nil {
		d := net.Dialer{Timeout: b.cfg.Timeout}
		dial = d.DialContext
	}
	conn, err := dial(ctx, "tcp", b.cfg.Address)
	if err != nil {
		return nil, fmt.Errorf("boinc: %w", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(b.cfg.Timeout))
	br := bufio.NewReader(conn)
	needAuth := b.cfg.Password != ""
	if !needAuth {
		host, _, _ := net.SplitHostPort(b.cfg.Address)
		ip := net.ParseIP(host)
		if host != "localhost" && host != "127.0.0.1" && host != "::1" && (ip == nil || !ip.IsLoopback()) {
			needAuth = true
		}
	}
	if needAuth {
		if err := boincAuth(conn, br, b.cfg.Password); err != nil {
			return nil, fmt.Errorf("boinc: %w", err)
		}
	}
	reply, err := boincSend(conn, br, `<boinc_gui_rpc_request><get_results><active_only>0</active_only></get_results></boinc_gui_rpc_request>`)
	if err != nil {
		return nil, fmt.Errorf("boinc: %w", err)
	}
	rs, err := parseBoincResults(reply)
	if err != nil {
		return nil, fmt.Errorf("boinc: %w", err)
	}
	return rs, nil
}

func boincAuth(conn net.Conn, br *bufio.Reader, password string) error {
	reply, err := boincSend(conn, br, `<boinc_gui_rpc_request><auth1/></boinc_gui_rpc_request>`)
	if err != nil {
		return err
	}
	nonce := xmlTag(reply, "nonce")
	if nonce == "" {
		return fmt.Errorf("auth1: empty nonce")
	}
	hash := fmt.Sprintf("%x", md5.Sum([]byte(nonce+password)))
	reply, err = boincSend(conn, br, `<boinc_gui_rpc_request><auth2><nonce_hash>`+hash+`</nonce_hash></auth2></boinc_gui_rpc_request>`)
	if err != nil {
		return err
	}
	if strings.Contains(reply, "<unauthorized") || !strings.Contains(reply, "<authorized") {
		return fmt.Errorf("auth2: unauthorized")
	}
	return nil
}

func boincSend(conn net.Conn, br *bufio.Reader, req string) (string, error) {
	if _, err := conn.Write(append([]byte(req), 3)); err != nil {
		return "", err
	}
	var b bytes.Buffer
	for {
		line, err := br.ReadString('\n')
		if err != nil && err != io.EOF {
			if b.Len() == 0 {
				return "", err
			}
			break
		}
		s := strings.TrimSpace(line)
		if s == "" {
			if err == io.EOF {
				break
			}
			continue
		}
		b.WriteString(s)
		if strings.Contains(s, "</boinc_gui_rpc_reply>") || strings.Contains(b.String(), "</boinc_gui_rpc_reply>") {
			break
		}
		if err == io.EOF {
			break
		}
	}
	out := b.String()
	if i := strings.IndexByte(out, 3); i >= 0 {
		out = out[:i]
	}
	if out == "" {
		return "", fmt.Errorf("empty reply")
	}
	return out, nil
}

type boincXMLReply struct {
	Results []struct {
		State      string `xml:"state"`
		ActiveTask *struct {
			ActiveTaskState string `xml:"active_task_state"`
			SchedulerState  string `xml:"scheduler_state"`
		} `xml:"active_task"`
	} `xml:"results>result"`
	Error        *struct{} `xml:"error"`
	Unauthorized *struct{} `xml:"unauthorized"`
	BadRequest   *struct{} `xml:"bad_request"`
}

func parseBoincResults(body string) ([]boincResult, error) {
	body = strings.ReplaceAll(body, `encoding="ISO-8859-1"`, `encoding="UTF-8"`)
	var reply boincXMLReply
	if err := xml.Unmarshal([]byte(body), &reply); err != nil {
		if strings.Contains(body, "<unauthorized") {
			return nil, fmt.Errorf("unauthorized")
		}
		return nil, err
	}
	if reply.Error != nil {
		return nil, fmt.Errorf("server error")
	}
	if reply.Unauthorized != nil {
		return nil, fmt.Errorf("unauthorized")
	}
	out := make([]boincResult, 0, len(reply.Results))
	for _, r := range reply.Results {
		item := boincResult{State: atoi(strings.TrimSpace(r.State))}
		if r.ActiveTask != nil {
			item.Active = true
			item.ActiveState = atoi(strings.TrimSpace(r.ActiveTask.ActiveTaskState))
			item.SchedulerState = atoi(strings.TrimSpace(r.ActiveTask.SchedulerState))
		}
		out = append(out, item)
	}
	return out, nil
}

func xmlTag(s, name string) string {
	open := "<" + name + ">"
	close := "</" + name + ">"
	i := strings.Index(s, open)
	if i < 0 {
		return ""
	}
	s = s[i+len(open):]
	j := strings.Index(s, close)
	if j < 0 {
		return ""
	}
	return s[:j]
}

func boincResultState(n int) string {
	switch n {
	case 1:
		return "files_downloading"
	case 2:
		return "files_downloaded"
	case 3:
		return "compute_error"
	case 4:
		return "files_uploading"
	case 5:
		return "files_uploaded"
	case 6:
		return "aborted"
	case 7:
		return "upload_failed"
	default:
		return "new"
	}
}

func boincActiveState(n int) string {
	switch n {
	case 1:
		return "executing"
	case 5:
		return "abort_pending"
	case 8:
		return "quit_pending"
	case 9:
		return "suspended"
	case 10:
		return "copy_pending"
	default:
		return "uninitialized"
	}
}
