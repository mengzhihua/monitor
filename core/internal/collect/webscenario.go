package collect

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// webscenario runs an ordered HTTP script. Later steps may use {{name}} values
// captured by an earlier step's extract regular expression.
type webscenarioConfig struct {
	Timeout time.Duration    `yaml:"timeout"`
	Jobs    []webscenarioJob `yaml:"jobs"`
}

type webscenarioJob struct {
	Name  string            `yaml:"name"`
	Steps []webscenarioStep `yaml:"steps"`
}

type webscenarioStep struct {
	Name    string            `yaml:"name"`
	URL     string            `yaml:"url"`
	Method  string            `yaml:"method"`
	Body    string            `yaml:"body"`
	Headers map[string]string `yaml:"headers"`
	Expect  int               `yaml:"expect"`
	Extract string            `yaml:"extract"`
	Var     string            `yaml:"var"`
}

type webscenarioCollector struct {
	cfg    webscenarioConfig
	client *http.Client
}

func init() { Register("webscenario", func() Collector { return &webscenarioCollector{} }) }

func (w *webscenarioCollector) Name() string { return "webscenario" }

func (w *webscenarioCollector) Configure(decode func(v any) error) error {
	if err := decode(&w.cfg); err != nil {
		return err
	}
	if w.cfg.Timeout <= 0 {
		w.cfg.Timeout = 8 * time.Second
	}
	for _, job := range w.cfg.Jobs {
		if !validItemName(job.Name) || len(job.Steps) == 0 || len(job.Steps) > 16 {
			return fmt.Errorf("webscenario: job %q needs 1..16 steps", job.Name)
		}
		for _, st := range job.Steps {
			if st.URL == "" {
				return fmt.Errorf("webscenario %s: step url required", job.Name)
			}
		}
	}
	return nil
}

func (w *webscenarioCollector) Init(reg *registry.Registry) error {
	if len(w.cfg.Jobs) == 0 {
		return fmt.Errorf("webscenario: no jobs")
	}
	w.client = &http.Client{Timeout: w.cfg.Timeout}
	for _, job := range w.cfg.Jobs {
		id := sanitizeID(job.Name)
		reg.AddChart(&registry.Chart{ID: "webscenario.status." + id, Context: "webscenario.status", Title: "Web scenario " + job.Name,
			Units: "status", Family: "web", Plugin: "webscenario", Module: "webscenario", Priority: 56500,
			Dimensions: []*registry.Dimension{{ID: "success"}, {ID: "failed_step"}}})
		reg.AddChart(&registry.Chart{ID: "webscenario.time." + id, Context: "webscenario.time", Title: "Web scenario time " + job.Name,
			Units: "seconds", Family: "web", Plugin: "webscenario", Module: "webscenario", Priority: 56510,
			Dimensions: []*registry.Dimension{{ID: "time"}}})
	}
	return nil
}

func (w *webscenarioCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	for _, job := range w.cfg.Jobs {
		ok, failed, elapsed := w.run(ctx, job)
		success, step := 0.0, float64(failed)
		if ok {
			success, step = 1, 0
		}
		id := sanitizeID(job.Name)
		_ = reg.Collect("webscenario.status."+id, now, map[string]float64{"success": success, "failed_step": step})
		_ = reg.Collect("webscenario.time."+id, now, map[string]float64{"time": elapsed})
	}
	return nil
}

func (w *webscenarioCollector) run(ctx context.Context, job webscenarioJob) (ok bool, failedStep int, elapsed float64) {
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Timeout: w.cfg.Timeout, Jar: jar}
	vars := map[string]string{}
	for i, st := range job.Steps {
		rawURL := expandVars(st.URL, vars)
		body := expandVars(st.Body, vars)
		headers := map[string]string{}
		for k, v := range st.Headers {
			headers[k] = expandVars(v, vars)
		}
		code, text, dt, err := httpTextCookie(ctx, client, st.Method, rawURL, headers, body, 1<<20)
		elapsed += dt
		expect := st.Expect
		if expect == 0 {
			expect = 200
		}
		if err != nil || code != expect {
			return false, i + 1, elapsed
		}
		if st.Extract != "" && st.Var != "" {
			re, err := regexp.Compile(st.Extract)
			if err != nil {
				return false, i + 1, elapsed
			}
			m := re.FindStringSubmatch(text)
			if m == nil {
				return false, i + 1, elapsed
			}
			val := m[0]
			if len(m) > 1 {
				val = m[1]
			}
			vars[st.Var] = val
		}
	}
	return true, 0, elapsed
}

// httpTextCookie is httpText for web scenarios. When the step sets Cookie
// explicitly, jar cookies are not attached (avoids duplicate Cookie headers);
// Set-Cookie from the response is still stored in the jar for later steps.
func httpTextCookie(ctx context.Context, client *http.Client, method, rawURL string, headers map[string]string, body string, limit int64) (code int, text string, elapsed float64, err error) {
	manualCookie := false
	for k := range headers {
		if strings.EqualFold(k, "Cookie") {
			manualCookie = true
			break
		}
	}
	if !manualCookie || client == nil || client.Jar == nil {
		return httpText(ctx, client, method, rawURL, headers, body, limit)
	}
	if method == "" {
		method = http.MethodGet
	}
	if limit <= 0 {
		limit = 1 << 20
	}
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, rdr)
	if err != nil {
		return 0, "", 0, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	send := *client
	send.Jar = nil
	start := time.Now()
	resp, err := send.Do(req)
	elapsed = time.Since(start).Seconds()
	if err != nil {
		return 0, "", elapsed, err
	}
	defer resp.Body.Close()
	if u, perr := url.Parse(rawURL); perr == nil {
		client.Jar.SetCookies(u, resp.Cookies())
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return resp.StatusCode, "", elapsed, err
	}
	if int64(len(b)) > limit {
		b = b[:limit]
	}
	return resp.StatusCode, string(b), elapsed, nil
}

func expandVars(s string, vars map[string]string) string {
	for k, v := range vars {
		s = strings.ReplaceAll(s, "{{"+k+"}}", v)
	}
	return s
}
