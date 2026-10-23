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
	// stopping gates handler registration before Stop waits for them.
	stopping bool
	handlers sync.WaitGroup
	done     chan struct{}
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
	o.done = make(chan struct{})
	go func() {
		defer close(o.done)
		_ = o.srv.Serve(ln)
	}()
	return nil
}

func (o *otlpCollector) Stop() {
	o.mu.Lock()
	o.stopping = true
	o.mu.Unlock()
	// Shutdown alone misses a listener if Serve has not started running yet.
	if o.ln != nil {
		_ = o.ln.Close()
	}
	if o.srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := o.srv.Shutdown(ctx); err != nil {
			_ = o.srv.Close()
		}
	}
	if o.done != nil {
		<-o.done
	}
	o.handlers.Wait()
}

func (o *otlpCollector) handle(w http.ResponseWriter, r *http.Request) {
	o.mu.Lock()
	if o.stopping {
		o.mu.Unlock()
		http.Error(w, "collector stopping", http.StatusServiceUnavailable)
		return
	}
	o.handlers.Add(1)
	reg := o.reg
	o.mu.Unlock()
	defer o.handlers.Done()
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
