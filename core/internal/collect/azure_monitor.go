package collect

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// azureMonitorConfig is collectors.modules.azure_monitor (OAuth + metrics REST).
type azureMonitorConfig struct {
	TenantID       string        `yaml:"tenant_id"`
	ClientID       string        `yaml:"client_id"`
	ClientSecret   string        `yaml:"client_secret"`
	SubscriptionID string        `yaml:"subscription_id"`
	ResourceID     string        `yaml:"resource_id"`
	Metric         string        `yaml:"metric"`
	LoginURL       string        `yaml:"login_url"`
	URL            string        `yaml:"url"`
	Timeout        time.Duration `yaml:"timeout"`
}

type azureMonitorCollector struct {
	cfg    azureMonitorConfig
	client *http.Client
	token  string
}

func init() {
	Register("azure_monitor", func() Collector { return &azureMonitorCollector{} })
}

func (a *azureMonitorCollector) Name() string { return "azure_monitor" }

func (a *azureMonitorCollector) Configure(decode func(v any) error) error {
	if err := decode(&a.cfg); err != nil {
		return err
	}
	if a.cfg.Metric == "" {
		a.cfg.Metric = "Percentage CPU"
	}
	if a.cfg.Timeout <= 0 {
		a.cfg.Timeout = 10 * time.Second
	}
	return nil
}

func (a *azureMonitorCollector) Init(reg *registry.Registry) error {
	if a.cfg.Timeout <= 0 {
		if err := a.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	if a.cfg.TenantID == "" || a.cfg.ClientID == "" || a.cfg.ClientSecret == "" {
		return fmt.Errorf("azure_monitor: no credentials")
	}
	if a.cfg.ResourceID == "" && a.cfg.SubscriptionID == "" {
		return fmt.Errorf("azure_monitor: no resource")
	}
	a.client = &http.Client{Timeout: a.cfg.Timeout}
	if err := a.login(context.Background()); err != nil {
		return err
	}
	if _, err := a.metric(context.Background()); err != nil {
		return err
	}
	ch := &registry.Chart{ID: "azure_monitor.metric", Context: "azure_monitor.metric", Title: "Azure Monitor metric",
		Units: "value", Family: "metrics", Priority: 62500, Dimensions: []*registry.Dimension{{ID: "average"}},
		Labels: map[string]string{"metric": a.cfg.Metric}}
	ch.Plugin, ch.Module = "azure_monitor", "azure_monitor"
	reg.AddChart(ch)
	return nil
}

func (a *azureMonitorCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	v, err := a.metric(ctx)
	if err != nil {
		_ = a.login(ctx)
		v, err = a.metric(ctx)
		if err != nil {
			return err
		}
	}
	_ = reg.Collect("azure_monitor.metric", now, map[string]float64{"average": v})
	return nil
}

func (a *azureMonitorCollector) login(ctx context.Context) error {
	login := a.cfg.LoginURL
	if login == "" {
		login = "https://login.microsoftonline.com/" + a.cfg.TenantID + "/oauth2/v2.0/token"
	}
	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	form.Set("client_id", a.cfg.ClientID)
	form.Set("client_secret", a.cfg.ClientSecret)
	form.Set("scope", "https://management.azure.com/.default")
	b, err := httpPostBody(ctx, a.client, login, "application/x-www-form-urlencoded", form.Encode(), nil)
	if err != nil {
		return fmt.Errorf("azure_monitor: %w", err)
	}
	m, err := jsonMap(b)
	if err != nil {
		return fmt.Errorf("azure_monitor: token json: %w", err)
	}
	tok := nestString(m, "access_token")
	if tok == "" {
		return fmt.Errorf("azure_monitor: no access_token")
	}
	a.token = tok
	return nil
}

func (a *azureMonitorCollector) metric(ctx context.Context) (float64, error) {
	base := strings.TrimRight(a.cfg.URL, "/")
	if base == "" {
		base = "https://management.azure.com"
	}
	rid := a.cfg.ResourceID
	if rid == "" {
		rid = "/subscriptions/" + a.cfg.SubscriptionID
	}
	u := base + rid + "/providers/microsoft.insights/metrics?api-version=2018-01-01&metricnames=" + url.QueryEscape(a.cfg.Metric) + "&aggregation=Average"
	b, err := httpGetToken(ctx, a.client, u, a.token)
	if err != nil {
		return 0, fmt.Errorf("azure_monitor: %w", err)
	}
	m, err := jsonMap(b)
	if err != nil {
		if v := xmlTag(string(b), "average"); v != "" {
			return firstFloat(v), nil
		}
		return 0, fmt.Errorf("azure_monitor: %w", err)
	}
	vals := nestSlice(m, "value")
	for _, item := range vals {
		series := nestSlice(item, "timeseries")
		for _, ts := range series {
			data := nestSlice(ts, "data")
			for i := len(data) - 1; i >= 0; i-- {
				n := nestFloat(data[i], "average")
				if m := nestMap(data[i]); m != nil {
					if _, ok := m["average"]; ok {
						return n, nil
					}
				}
			}
		}
	}
	if n := nestFloat(m, "average"); n != 0 {
		return n, nil
	}
	return 0, fmt.Errorf("azure_monitor: no datapoints")
}
