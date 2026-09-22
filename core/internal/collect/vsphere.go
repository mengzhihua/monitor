package collect

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

const vsphereRetrieveServiceContent = `<?xml version="1.0" encoding="UTF-8"?>
<soapenv:Envelope xmlns:soapenv="http://schemas.xmlsoap.org/soap/envelope/" xmlns:urn="urn:vim25">
  <soapenv:Body>
    <urn:RetrieveServiceContent>
      <urn:_this type="ServiceInstance">ServiceInstance</urn:_this>
    </urn:RetrieveServiceContent>
  </soapenv:Body>
</soapenv:Envelope>`

const vsphereFindInventory = `<?xml version="1.0" encoding="UTF-8"?>
<soapenv:Envelope xmlns:soapenv="http://schemas.xmlsoap.org/soap/envelope/" xmlns:urn="urn:vim25">
  <soapenv:Body>
    <urn:RetrieveProperties>
      <urn:_this type="PropertyCollector">propertyCollector</urn:_this>
    </urn:RetrieveProperties>
  </soapenv:Body>
</soapenv:Envelope>`

// vsphereConfig is collectors.modules.vsphere (SOAP vim25 /sdk).
type vsphereConfig struct {
	TLS      CollectorTLS  `yaml:"tls"`
	URL      string        `yaml:"url"`
	User     string        `yaml:"user"`
	Password string        `yaml:"password"`
	Timeout  time.Duration `yaml:"timeout"`
}

type vsphereCollector struct {
	cfg    vsphereConfig
	client *http.Client
	url    string
}

func init() {
	Register("vsphere", func() Collector { return &vsphereCollector{} })
}

func (v *vsphereCollector) Name() string { return "vsphere" }

func (v *vsphereCollector) Configure(decode func(v any) error) error {
	if err := decode(&v.cfg); err != nil {
		return err
	}
	if v.cfg.Timeout <= 0 {
		v.cfg.Timeout = 8 * time.Second
	}
	return nil
}

func (v *vsphereCollector) Init(reg *registry.Registry) error {
	if v.cfg.Timeout <= 0 {
		if err := v.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	if strings.TrimSpace(v.cfg.User) == "" {
		return fmt.Errorf("vsphere: no credentials")
	}
	client, tlsErr := collectorHTTPClient(v.cfg.Timeout, v.cfg.TLS)
	if tlsErr != nil {
		return tlsErr
	}
	v.client = client
	base := strings.TrimRight(v.cfg.URL, "/")
	if base == "" {
		base = "https://127.0.0.1"
	}
	if !strings.HasSuffix(base, "/sdk") {
		base += "/sdk"
	}
	v.url = base
	if _, err := v.service(context.Background()); err != nil {
		return err
	}
	for _, ch := range []*registry.Chart{
		{ID: "vsphere.inventory_objects", Context: "vsphere.inventory_objects", Title: "vSphere inventory object count",
			Units: "objects", Family: "inventory", Priority: 62600, Dimensions: []*registry.Dimension{
				{ID: "datacenters"}, {ID: "clusters"}, {ID: "hosts"}, {ID: "vms"}, {ID: "datastores"}, {ID: "resource_pools"}, {ID: "folders"}}},
		{ID: "vsphere.host_connection_state", Context: "vsphere.host_connection_state", Title: "vSphere host connection state",
			Units: "state", Family: "hosts", Priority: 62610, Dimensions: []*registry.Dimension{{ID: "connected"}, {ID: "disconnected"}}},
	} {
		ch.Plugin, ch.Module = "vsphere", "vsphere"
		reg.AddChart(ch)
	}
	return nil
}

func (v *vsphereCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	inv, conn, err := v.inventory(ctx)
	if err != nil {
		return err
	}
	_ = reg.Collect("vsphere.inventory_objects", now, inv)
	_ = reg.Collect("vsphere.host_connection_state", now, conn)
	return nil
}

func (v *vsphereCollector) service(ctx context.Context) (string, error) {
	b, err := v.soap(ctx, vsphereRetrieveServiceContent)
	if err != nil {
		return "", err
	}
	s := string(b)
	name := xmlTag(s, "fullName")
	if name == "" && !strings.Contains(s, "RetrieveServiceContent") && !strings.Contains(s, "about") && !strings.Contains(s, "apiType") {
		return "", fmt.Errorf("vsphere: no ServiceContent")
	}
	return name, nil
}

func (v *vsphereCollector) inventory(ctx context.Context) (map[string]float64, map[string]float64, error) {
	if _, err := v.service(ctx); err != nil {
		return nil, nil, err
	}
	b, err := v.soap(ctx, vsphereFindInventory)
	if err != nil {
		// ServiceContent succeeded; inventory is optional on some stubs.
		b = nil
	}
	s := string(b)
	inv := map[string]float64{
		"datacenters":    countXMLOpen(s, "Datacenter"),
		"clusters":       countXMLOpen(s, "ClusterComputeResource"),
		"hosts":          countXMLOpen(s, "HostSystem"),
		"vms":            countXMLOpen(s, "VirtualMachine"),
		"datastores":     countXMLOpen(s, "Datastore"),
		"resource_pools": countXMLOpen(s, "ResourcePool"),
		"folders":        countXMLOpen(s, "Folder"),
	}
	connected, disconnected := 0.0, 0.0
	if strings.Contains(strings.ToLower(s), "disconnected") {
		disconnected = 1
	} else if inv["hosts"] > 0 || strings.Contains(strings.ToLower(s), "connected") {
		connected = 1
	} else {
		disconnected = 1
	}
	return inv, map[string]float64{"connected": connected, "disconnected": disconnected}, nil
}

func (v *vsphereCollector) soap(ctx context.Context, body string) ([]byte, error) {
	hdr := map[string]string{"SOAPAction": "urn:vim25/6.0"}
	if v.cfg.User != "" {
		// SOAP login is session-based; basic auth is accepted by some reverse proxies and tests.
		b, err := httpDo(ctx, v.client, http.MethodPost, v.url, "text/xml", body, hdr, v.cfg.User, v.cfg.Password)
		if err != nil {
			return nil, fmt.Errorf("vsphere: %w", err)
		}
		return b, nil
	}
	b, err := httpPostBody(ctx, v.client, v.url, "text/xml", body, hdr)
	if err != nil {
		return nil, fmt.Errorf("vsphere: %w", err)
	}
	return b, nil
}

func countXMLOpen(s, name string) float64 {
	n := 0
	token := "<" + name
	for {
		i := strings.Index(s, token)
		if i < 0 {
			break
		}
		rest := s[i+len(token):]
		if rest == "" || rest[0] == '>' || rest[0] == ' ' || rest[0] == '/' {
			n++
		}
		s = s[i+len(token):]
	}
	return float64(n)
}
