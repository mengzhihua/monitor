package collect

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/mengzhihua/monitor/core/internal/ingest"
	"github.com/mengzhihua/monitor/core/internal/registry"
)

// otlpConfig is collectors.modules.otlp — an HTTP listener for OTLP metrics
// (JSON or protobuf) on the standard /v1/metrics path.
type otlpConfig struct {
	Listen string `yaml:"listen"` // default 127.0.0.1:4318; empty disables
}

type otlpCollector struct {
	cfg    otlpConfig
	srv    *http.Server
	ln     net.Listener
	mapper *ingest.Mapper
	mu     sync.Mutex
	reg    *registry.Registry
}

func init() {
	Register("otlp", func() Collector { return &otlpCollector{} })
}

func (o *otlpCollector) Name() string { return "otlp" }

func (o *otlpCollector) Configure(decode func(v any) error) error {
	if err := decode(&o.cfg); err != nil {
		return err
	}
	if o.cfg.Listen == "" {
		o.cfg.Listen = "127.0.0.1:4318"
	}
	return nil
}

func (o *otlpCollector) Init(reg *registry.Registry) error {
	if o.cfg.Listen == "" {
		if err := o.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	o.reg = reg
	o.mapper = ingest.NewMapper(ingest.Options{Prefix: "otlp", Plugin: "otlp", Module: "otlp", Family: "otlp"})
	ln, err := net.Listen("tcp", o.cfg.Listen)
	if err != nil {
		return err
	}
	o.ln = ln
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/metrics", o.handle)
	mux.HandleFunc("POST /", o.handle)
	o.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = o.srv.Serve(ln) }()
	return nil
}

func (o *otlpCollector) Stop() {
	if o.srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = o.srv.Shutdown(ctx)
	}
}

func (o *otlpCollector) handle(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	samples, err := ingest.ParseOTLP(body, r.Header.Get("Content-Type"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	o.mu.Lock()
	reg := o.reg
	o.mu.Unlock()
	if reg != nil {
		o.mapper.Apply(reg, time.Now(), samples)
	}
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprint(w, `{"partialSuccess":{}}`)
}

func (o *otlpCollector) Collect(context.Context, *registry.Registry, time.Time) error {
	return nil
}

func (o *otlpCollector) Addr() string {
	if o.ln == nil {
		return o.cfg.Listen
	}
	return o.ln.Addr().String()
}

func (o *otlpCollector) Ingest(reg *registry.Registry, body []byte, contentType string) (int, error) {
	samples, err := ingest.ParseOTLP(body, contentType)
	if err != nil {
		return 0, err
	}
	if o.mapper == nil {
		o.mapper = ingest.NewMapper(ingest.Options{Prefix: "otlp", Plugin: "otlp", Module: "otlp", Family: "otlp"})
	}
	return o.mapper.Apply(reg, time.Now(), samples), nil
}
