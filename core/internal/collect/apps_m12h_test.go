package collect

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

func TestM12HCollectorsRegistered(t *testing.T) {
	for _, name := range []string{
		"vcsa", "mssql", "oracledb", "sql", "cloudwatch",
		"azure_monitor", "vsphere", "cato_networks", "snmp_traps", "snmp_topology",
	} {
		found := false
		for _, n := range Available() {
			if n == name {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("collector %s not registered", name)
		}
	}
}

func TestVCSAVSphereCatoCloudAzure(t *testing.T) {
	vc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "session") {
			_, _ = w.Write([]byte(`{"value":"sess"}`))
			return
		}
		_, _ = w.Write([]byte(`{"value":"green"}`))
	}))
	defer vc.Close()
	v := &vcsaCollector{cfg: vcsaConfig{URL: vc.URL, User: "root", Password: "pw", Timeout: time.Second}}
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	if err := v.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := v.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Chart("vcsa.system_health_status"); !ok {
		t.Fatal("missing vcsa chart")
	}

	vs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(string(mustRead(r)), "RetrieveServiceContent") {
			_, _ = w.Write([]byte(`<Envelope><Body><RetrieveServiceContentResponse><returnval><about><fullName>VMware vCenter Server 8.0</fullName><apiType>VirtualCenter</apiType></about></returnval></RetrieveServiceContentResponse></Body></Envelope>`))
			return
		}
		_, _ = w.Write([]byte(`<Envelope><Body><HostSystem>h1</HostSystem><HostSystem>h2</HostSystem><VirtualMachine>vm1</VirtualMachine><Datastore>ds1</Datastore><Datacenter>dc1</Datacenter></Body></Envelope>`))
	}))
	defer vs.Close()
	sp := &vsphereCollector{cfg: vsphereConfig{URL: vs.URL, User: "admin", Password: "pw", Timeout: time.Second}}
	if err := sp.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := sp.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}

	ca := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"accountSnapshot":{"sites":[{"id":"s1","name":"hq","hosts":3,"connectivityStatus":"connected","operationalStatus":"active","bytesUpstreamMax":100,"bytesDownstreamMax":200,"lostUpstreamPercent":0.1,"lostDownstreamPercent":0.2,"rttMs":12}]}}}`))
	}))
	defer ca.Close()
	c := &catoNetworksCollector{cfg: catoNetworksConfig{URL: ca.URL, APIKey: "k", AccountID: "a1", Timeout: time.Second}}
	if err := c.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := c.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}

	cw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<GetMetricStatisticsResponse><GetMetricStatisticsResult><Datapoints><member><Average>12.5</Average></member></Datapoints></GetMetricStatisticsResult></GetMetricStatisticsResponse>`))
	}))
	defer cw.Close()
	w := &cloudwatchCollector{cfg: cloudwatchConfig{AccessKey: "AK", SecretKey: "SK", Region: "us-east-1", Endpoint: cw.URL, Timeout: time.Second}}
	if err := w.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := w.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}

	az := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "token") || r.Method == http.MethodPost {
			_, _ = w.Write([]byte(`{"access_token":"tok","expires_in":3600}`))
			return
		}
		_, _ = w.Write([]byte(`{"value":[{"timeseries":[{"data":[{"average":18.5}]}]}]}`))
	}))
	defer az.Close()
	a := &azureMonitorCollector{cfg: azureMonitorConfig{
		TenantID: "t", ClientID: "c", ClientSecret: "s", ResourceID: "/subscriptions/x/resourceGroups/r/providers/Microsoft.Compute/virtualMachines/vm",
		LoginURL: az.URL + "/token", URL: az.URL, Timeout: time.Second,
	}}
	if err := a.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := a.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestMSSQLOracleSQL(t *testing.T) {
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	m := &mssqlCollector{
		cfg: mssqlConfig{Address: "127.0.0.1,1433", User: "sa", Command: "sqlcmd", Timeout: time.Second},
		run: func(context.Context, string, ...string) ([]byte, error) {
			return []byte("User Connections 12\nProcesses blocked 1\nBatch Requests/sec 100\nSQL Compilations/sec 5\nSQL Re-Compilations/sec 1\nBuffer cache hit ratio 98\nUser connection count 10\nInternal connection count 2\n"), nil
		},
	}
	if err := m.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := m.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}

	o := &oracledbCollector{
		cfg: oracledbConfig{DSN: "u/p@//127.0.0.1:1521/orcl", Command: "sqlplus", Timeout: time.Second},
		run: func(context.Context, string, ...string) ([]byte, error) {
			return []byte("sessions 15\nactive 3\nsession_limit 12.5\nparse 100\nexecute 200\nuser_commits 50\nuser_rollbacks 1\n"), nil
		},
	}
	if err := o.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := o.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}

	s := &sqlCollector{
		cfg: sqlConfig{Driver: "mysql", DSN: "-h 127.0.0.1", Query: "SELECT 1 AS value", Command: "mysql", Timeout: time.Second},
		run: func(context.Context, string, ...string) ([]byte, error) {
			return []byte("value 42\n"), nil
		},
	}
	if err := s.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := s.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Chart("sql.mysql_query"); !ok {
		t.Fatal("missing sql chart")
	}
}

func TestSNMPTrapsAndTopology(t *testing.T) {
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	tr := &snmpTrapsCollector{cfg: snmpTrapsConfig{Listen: "127.0.0.1:0"}}
	if err := tr.Init(reg); err != nil {
		t.Fatal(err)
	}
	defer tr.Stop()
	addr := tr.conn.LocalAddr().String()
	c, err := net.Dial("udp", addr)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Write(testSNMPv2cTrap()); err != nil {
		t.Fatal(err)
	}
	_ = c.Close()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		tr.mu.Lock()
		n := tr.received
		tr.mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := tr.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}
	tr.mu.Lock()
	got := tr.received
	tr.mu.Unlock()
	if got < 1 {
		t.Fatalf("expected trap received, got %v", got)
	}

	tp := &snmpTopologyCollector{
		cfg: snmpTopologyConfig{Address: "127.0.0.1", Community: "public", Version: "2c", Command: "snmpwalk", Timeout: time.Second},
		run: func(context.Context, string, ...string) ([]byte, error) {
			return []byte("1.0.8802.1.1.2.1.4.1.1.9.0.1.1 = STRING: \"switch-a\"\n1.0.8802.1.1.2.1.4.1.1.9.0.1.2 = STRING: \"switch-b\"\n"), nil
		},
	}
	if err := tp.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := tp.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Chart("snmp_topology.devices"); !ok {
		t.Fatal("missing topology chart")
	}
}

func TestM12HAutoDisable(t *testing.T) {
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	if err := (&vcsaCollector{cfg: vcsaConfig{Timeout: 50 * time.Millisecond}}).Init(reg); err == nil {
		t.Fatal("vcsa should disable without credentials")
	}
	if err := (&cloudwatchCollector{cfg: cloudwatchConfig{Timeout: 50 * time.Millisecond}}).Init(reg); err == nil {
		t.Fatal("cloudwatch should disable without keys")
	}
	if err := (&azureMonitorCollector{cfg: azureMonitorConfig{Timeout: 50 * time.Millisecond}}).Init(reg); err == nil {
		t.Fatal("azure_monitor should disable without credentials")
	}
	if err := (&catoNetworksCollector{cfg: catoNetworksConfig{Timeout: 50 * time.Millisecond}}).Init(reg); err == nil {
		t.Fatal("cato_networks should disable without api_key")
	}
	if err := (&oracledbCollector{cfg: oracledbConfig{Timeout: 50 * time.Millisecond}}).Init(reg); err == nil {
		t.Fatal("oracledb should disable without dsn")
	}
	if err := (&sqlCollector{cfg: sqlConfig{Timeout: 50 * time.Millisecond}}).Init(reg); err == nil {
		t.Fatal("sql should disable without query")
	}
	if err := (&vsphereCollector{cfg: vsphereConfig{Timeout: 50 * time.Millisecond}}).Init(reg); err == nil {
		t.Fatal("vsphere should disable without credentials")
	}
	if err := (&mssqlCollector{cfg: mssqlConfig{Command: "sqlcmd", Timeout: 50 * time.Millisecond}, run: func(context.Context, string, ...string) ([]byte, error) {
		return nil, context.DeadlineExceeded
	}}).Init(reg); err == nil {
		t.Fatal("mssql should disable when sqlcmd fails")
	}
	if err := (&snmpTopologyCollector{cfg: snmpTopologyConfig{Command: "snmpwalk", Timeout: 50 * time.Millisecond}, run: func(context.Context, string, ...string) ([]byte, error) {
		return []byte(""), nil
	}}).Init(reg); err == nil {
		t.Fatal("snmp_topology should disable without neighbors")
	}
}

func mustRead(r *http.Request) []byte {
	if r.Body == nil {
		return nil
	}
	b, _ := io.ReadAll(r.Body)
	return b
}

func testSNMPv2cTrap() []byte {
	inner := []byte{
		0x02, 0x01, 0x01, // version v2c
		0x04, 0x06, 'p', 'u', 'b', 'l', 'i', 'c',
		0xA7, 0x0C,
		0x02, 0x01, 0x00,
		0x02, 0x01, 0x00,
		0x02, 0x01, 0x00,
		0x30, 0x00,
	}
	out := make([]byte, 0, 2+len(inner))
	out = append(out, 0x30, byte(len(inner)))
	return append(out, inner...)
}
