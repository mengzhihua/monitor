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

// tomcatConfig is collectors.modules.tomcat (manager status XML).
type tomcatConfig struct {
	URL      string        `yaml:"url"`
	User     string        `yaml:"user"`
	Password string        `yaml:"password"`
	Timeout  time.Duration `yaml:"timeout"`
}

type tomcatCollector struct {
	cfg    tomcatConfig
	client *http.Client
	url    string
}

func init() {
	Register("tomcat", func() Collector { return &tomcatCollector{} })
}

func (t *tomcatCollector) Name() string { return "tomcat" }

func (t *tomcatCollector) Configure(decode func(v any) error) error {
	if err := decode(&t.cfg); err != nil {
		return err
	}
	if t.cfg.Timeout <= 0 {
		t.cfg.Timeout = 2 * time.Second
	}
	return nil
}

func (t *tomcatCollector) Init(reg *registry.Registry) error {
	if t.cfg.Timeout <= 0 {
		if err := t.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	t.client = &http.Client{Timeout: t.cfg.Timeout}
	urls := []string{t.cfg.URL}
	if t.cfg.URL == "" {
		urls = []string{
			"http://127.0.0.1:8080/manager/status?XML=true",
			"http://127.0.0.1:8080/manager/status?XML",
			"http://127.0.0.1:8080/status?XML=true",
		}
	}
	var last error
	for _, u := range urls {
		if u == "" {
			continue
		}
		b, err := httpGetAuth(context.Background(), t.client, u, t.cfg.User, t.cfg.Password)
		if err != nil {
			last = err
			continue
		}
		if _, err := parseTomcatStatus(b); err != nil {
			last = err
			continue
		}
		t.url = u
		break
	}
	if t.url == "" {
		if last == nil {
			last = fmt.Errorf("tomcat: no status XML")
		}
		return last
	}
	inc := registry.Incremental
	for _, c := range []*registry.Chart{
		{ID: "tomcat.jvm_memory_usage", Title: "JVM Memory Usage", Units: "bytes", Type: registry.Stacked, Priority: 51400,
			Dimensions: []*registry.Dimension{{ID: "free"}, {ID: "used"}}},
		{ID: "tomcat.connector_request_threads", Title: "Connector Request Threads", Units: "threads", Type: registry.Stacked, Priority: 51410,
			Dimensions: []*registry.Dimension{{ID: "idle"}, {ID: "busy"}}},
		{ID: "tomcat.connector_requests", Title: "Connector Requests", Units: "requests/s", Priority: 51420,
			Dimensions: []*registry.Dimension{{ID: "requests", Algorithm: inc}}},
		{ID: "tomcat.connector_errors", Title: "Connector Errors", Units: "errors/s", Priority: 51421,
			Dimensions: []*registry.Dimension{{ID: "errors", Algorithm: inc}}},
		{ID: "tomcat.connector_requests_processing_time", Title: "Connector Requests Processing Time", Units: "milliseconds", Priority: 51430,
			Dimensions: []*registry.Dimension{{ID: "processing_time", Algorithm: inc}}},
		{ID: "tomcat.connector_bandwidth", Title: "Connector Requests Bandwidth", Units: "bytes/s", Type: registry.Area, Priority: 51440,
			Dimensions: []*registry.Dimension{{ID: "received", Algorithm: inc}, {ID: "sent", Algorithm: inc, Multiplier: -1}}},
	} {
		c.Family, c.Plugin, c.Module = "tomcat", "tomcat", "tomcat"
		reg.AddChart(c)
	}
	return nil
}

func (t *tomcatCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	b, err := httpGetAuth(ctx, t.client, t.url, t.cfg.User, t.cfg.Password)
	if err != nil {
		return err
	}
	st, err := parseTomcatStatus(b)
	if err != nil {
		return err
	}
	used := st.memTotal - st.memFree
	if used < 0 {
		used = 0
	}
	idle := st.threads - st.busy
	if idle < 0 {
		idle = 0
	}
	_ = reg.Collect("tomcat.jvm_memory_usage", now, map[string]float64{"free": st.memFree, "used": used})
	_ = reg.Collect("tomcat.connector_request_threads", now, map[string]float64{"idle": idle, "busy": st.busy})
	_ = reg.Collect("tomcat.connector_requests", now, map[string]float64{"requests": st.requests})
	_ = reg.Collect("tomcat.connector_errors", now, map[string]float64{"errors": st.errors})
	_ = reg.Collect("tomcat.connector_requests_processing_time", now, map[string]float64{"processing_time": st.procTime})
	_ = reg.Collect("tomcat.connector_bandwidth", now, map[string]float64{"received": st.bytesIn, "sent": st.bytesOut})
	return nil
}

type tomcatStatus struct {
	memFree, memTotal, memMax                     float64
	threads, busy, maxThreads                     float64
	requests, errors, procTime, bytesIn, bytesOut float64
}

type tomcatXML struct {
	JVM struct {
		Memory struct {
			Free  float64 `xml:"free,attr"`
			Total float64 `xml:"total,attr"`
			Max   float64 `xml:"max,attr"`
		} `xml:"memory"`
	} `xml:"jvm"`
	Connectors []struct {
		ThreadInfo struct {
			Current float64 `xml:"currentThreadCount,attr"`
			Busy    float64 `xml:"currentThreadsBusy,attr"`
			Max     float64 `xml:"maxThreads,attr"`
		} `xml:"threadInfo"`
		RequestInfo struct {
			Processing float64 `xml:"processingTime,attr"`
			Requests   float64 `xml:"requestCount,attr"`
			Errors     float64 `xml:"errorCount,attr"`
			BytesIn    float64 `xml:"bytesReceived,attr"`
			BytesOut   float64 `xml:"bytesSent,attr"`
		} `xml:"requestInfo"`
	} `xml:"connector"`
}

func parseTomcatStatus(b []byte) (tomcatStatus, error) {
	s := strings.TrimSpace(string(b))
	if !strings.Contains(s, "<status") && !strings.Contains(s, "<jvm") {
		return tomcatStatus{}, fmt.Errorf("tomcat: not status XML")
	}
	var x tomcatXML
	if err := xml.Unmarshal(b, &x); err != nil {
		return tomcatStatus{}, fmt.Errorf("tomcat xml: %w", err)
	}
	st := tomcatStatus{memFree: x.JVM.Memory.Free, memTotal: x.JVM.Memory.Total, memMax: x.JVM.Memory.Max}
	for _, c := range x.Connectors {
		st.threads += c.ThreadInfo.Current
		st.busy += c.ThreadInfo.Busy
		st.maxThreads += c.ThreadInfo.Max
		st.requests += c.RequestInfo.Requests
		st.errors += c.RequestInfo.Errors
		st.procTime += c.RequestInfo.Processing
		st.bytesIn += c.RequestInfo.BytesIn
		st.bytesOut += c.RequestInfo.BytesOut
	}
	return st, nil
}
