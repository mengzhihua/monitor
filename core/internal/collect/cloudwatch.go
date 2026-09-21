package collect

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// cloudwatchConfig is collectors.modules.cloudwatch (AWS Query API + SigV4).
type cloudwatchConfig struct {
	AccessKey  string        `yaml:"access_key"`
	SecretKey  string        `yaml:"secret_key"`
	Region     string        `yaml:"region"`
	Namespace  string        `yaml:"namespace"`
	MetricName string        `yaml:"metric_name"`
	Dimension  string        `yaml:"dimension"` // Name=Value
	Endpoint   string        `yaml:"endpoint"`
	Timeout    time.Duration `yaml:"timeout"`
}

type cloudwatchCollector struct {
	cfg    cloudwatchConfig
	client *http.Client
	calls  float64
}

func init() {
	Register("cloudwatch", func() Collector { return &cloudwatchCollector{} })
}

func (c *cloudwatchCollector) Name() string { return "cloudwatch" }

func (c *cloudwatchCollector) Configure(decode func(v any) error) error {
	if err := decode(&c.cfg); err != nil {
		return err
	}
	if c.cfg.Region == "" {
		c.cfg.Region = "us-east-1"
	}
	if c.cfg.Namespace == "" {
		c.cfg.Namespace = "AWS/EC2"
	}
	if c.cfg.MetricName == "" {
		c.cfg.MetricName = "CPUUtilization"
	}
	if c.cfg.Timeout <= 0 {
		c.cfg.Timeout = 10 * time.Second
	}
	return nil
}

func (c *cloudwatchCollector) Init(reg *registry.Registry) error {
	if c.cfg.Timeout <= 0 {
		if err := c.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	if c.cfg.AccessKey == "" || c.cfg.SecretKey == "" {
		return fmt.Errorf("cloudwatch: no credentials")
	}
	c.client = &http.Client{Timeout: c.cfg.Timeout}
	if _, err := c.metric(context.Background()); err != nil {
		return err
	}
	for _, ch := range []*registry.Chart{
		{ID: "cloudwatch.metric", Context: "cloudwatch.metric", Title: "CloudWatch metric", Units: "value", Family: "metrics", Priority: 62400,
			Dimensions: []*registry.Dimension{{ID: "average"}}, Labels: map[string]string{"namespace": c.cfg.Namespace, "metric": c.cfg.MetricName}},
		{ID: "cloudwatch.collector_api_calls", Context: "cloudwatch.collector_api_calls", Title: "CloudWatch API calls", Units: "calls", Family: "Collector Activity", Priority: 62410,
			Dimensions: []*registry.Dimension{{ID: "calls", Algorithm: registry.Incremental}}},
	} {
		ch.Plugin, ch.Module = "cloudwatch", "cloudwatch"
		reg.AddChart(ch)
	}
	return nil
}

func (c *cloudwatchCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	v, err := c.metric(ctx)
	if err != nil {
		return err
	}
	_ = reg.Collect("cloudwatch.metric", now, map[string]float64{"average": v})
	_ = reg.Collect("cloudwatch.collector_api_calls", now, map[string]float64{"calls": c.calls})
	return nil
}

func (c *cloudwatchCollector) metric(ctx context.Context) (float64, error) {
	end := time.Now().UTC()
	start := end.Add(-5 * time.Minute)
	vals := url.Values{}
	vals.Set("Action", "GetMetricStatistics")
	vals.Set("Version", "2010-08-01")
	vals.Set("Namespace", c.cfg.Namespace)
	vals.Set("MetricName", c.cfg.MetricName)
	vals.Set("StartTime", start.Format(time.RFC3339))
	vals.Set("EndTime", end.Format(time.RFC3339))
	vals.Set("Period", "60")
	vals.Set("Statistics.member.1", "Average")
	if d := strings.TrimSpace(c.cfg.Dimension); d != "" {
		name, value, ok := strings.Cut(d, "=")
		if ok {
			vals.Set("Dimensions.member.1.Name", name)
			vals.Set("Dimensions.member.1.Value", value)
		}
	}
	host := "monitoring." + c.cfg.Region + ".amazonaws.com"
	endpoint := c.cfg.Endpoint
	if endpoint == "" {
		endpoint = "https://" + host
	}
	body := vals.Encode()
	hdr := map[string]string{}
	u, err := url.Parse(endpoint)
	if err != nil {
		return 0, err
	}
	if u.Host != "" {
		host = u.Host
	}
	awsSignV4("POST", "/", body, host, c.cfg.Region, "monitoring", c.cfg.AccessKey, c.cfg.SecretKey, hdr)
	c.calls++
	b, err := httpPostBody(ctx, c.client, strings.TrimRight(endpoint, "/")+"/", "application/x-www-form-urlencoded", body, hdr)
	if err != nil {
		return 0, fmt.Errorf("cloudwatch: %w", err)
	}
	s := string(b)
	if v := xmlTag(s, "Average"); v != "" {
		return firstFloat(v), nil
	}
	if m, err := jsonMap(b); err == nil {
		if n := nestFloat(m, "Average"); n != 0 {
			return n, nil
		}
		if n := nestFloat(m, "average"); n != 0 {
			return n, nil
		}
		pts := nestSlice(m, "Datapoints")
		if len(pts) == 0 {
			pts = nestSlice(m, "datapoints")
		}
		if len(pts) > 0 {
			return nestFloat(pts[len(pts)-1], "Average") + nestFloat(pts[len(pts)-1], "average"), nil
		}
	}
	if strings.Contains(s, "GetMetricStatistics") || strings.Contains(s, "Datapoints") {
		return 0, nil
	}
	return 0, fmt.Errorf("cloudwatch: no datapoints")
}

func awsSignV4(method, uri, payload, host, region, service, access, secret string, hdr map[string]string) {
	now := time.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	date := now.Format("20060102")
	payloadHash := sha256Hex([]byte(payload))
	hdr["Host"] = host
	hdr["X-Amz-Date"] = amzDate
	hdr["X-Amz-Content-Sha256"] = payloadHash
	signed := []string{"content-type", "host", "x-amz-content-sha256", "x-amz-date"}
	canonHdr := "content-type:application/x-www-form-urlencoded\nhost:" + strings.ToLower(host) + "\nx-amz-content-sha256:" + payloadHash + "\nx-amz-date:" + amzDate + "\n"
	signedStr := strings.Join(signed, ";")
	canonical := method + "\n" + uri + "\n\n" + canonHdr + "\n" + signedStr + "\n" + payloadHash
	scope := date + "/" + region + "/" + service + "/aws4_request"
	sts := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + sha256Hex([]byte(canonical))
	kDate := hmacSHA256([]byte("AWS4"+secret), date)
	kRegion := hmacSHA256(kDate, region)
	kService := hmacSHA256(kRegion, service)
	kSigning := hmacSHA256(kService, "aws4_request")
	sig := hex.EncodeToString(hmacSHA256(kSigning, sts))
	hdr["Authorization"] = "AWS4-HMAC-SHA256 Credential=" + access + "/" + scope + ", SignedHeaders=" + signedStr + ", Signature=" + sig
}

func hmacSHA256(key []byte, data string) []byte {
	m := hmac.New(sha256.New, key)
	_, _ = m.Write([]byte(data))
	return m.Sum(nil)
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
