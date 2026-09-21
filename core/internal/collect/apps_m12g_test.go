package collect

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

func TestM12GCollectorsRegistered(t *testing.T) {
	for _, name := range []string{
		"ap", "dockerhub", "ethtool", "intelgpu", "logind", "dcgm",
		"panos", "powerstore", "powervault", "s3check", "scaleio", "smbios_memory",
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

func TestAPEthtoolIntelGPULogindSMBIOS(t *testing.T) {
	a := &apCollector{
		cfg: apConfig{Command: "iw", Timeout: time.Second},
		run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			if len(args) == 1 && args[0] == "dev" {
				return []byte("phy#0\n\tInterface wlan0\n\t\tssid netdata\n\t\ttype AP\n"), nil
			}
			return []byte("Station aa:bb:cc:dd:ee:ff (on wlan0)\n\trx bytes:\t100\n\trx packets:\t10\n\ttx bytes:\t200\n\ttx packets:\t20\n\ttx retries:\t1\n\ttx failed:\t0\n\tsignal:\t-40 dBm\n\ttx bitrate:\t72.2 MBit/s\n\trx bitrate:\t65.0 MBit/s\n"), nil
		},
	}
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	if err := a.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := a.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}

	e := &ethtoolCollector{
		cfg: ethtoolConfig{Command: "ethtool", OpticalInterfaces: "eth0", Timeout: time.Second},
		run: func(context.Context, string, ...string) ([]byte, error) {
			return []byte("Module temperature : 32.00 degrees C / 89.60 degrees F\nModule voltage : 3.2945 V\nLaser tx bias current : 6.000 mA\nLaser tx power : 0.5432 mW / -2.65 dBm\nLaser rx power : 0.4123 mW / -3.85 dBm\n"), nil
		},
	}
	if err := e.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := e.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}

	g := &intelgpuCollector{
		cfg: intelgpuConfig{Command: "intel_gpu_top", Timeout: time.Second},
		run: func(context.Context, string, ...string) ([]byte, error) {
			return []byte(`{"engines":{"Render/3D/0":{"busy":12.5},"Blitter/0":{"busy":1}},"frequency":{"actual":300,"requested":300},"power":{"GPU":1.2,"Package":8.5}}`), nil
		},
	}
	if err := g.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := g.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}

	l := &logindCollector{
		cfg: logindConfig{Command: "loginctl", Timeout: time.Second},
		run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			if len(args) > 0 && args[0] == "list-sessions" {
				return []byte("c1 1000 user seat0\n"), nil
			}
			if len(args) > 0 && args[0] == "list-users" {
				return []byte("1000 user\n"), nil
			}
			if len(args) > 0 && args[0] == "show-session" {
				return []byte("Type=tty\nRemote=no\nState=active\n"), nil
			}
			return []byte("State=active\n"), nil
		},
	}
	if err := l.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := l.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}

	sm := &smbiosMemoryCollector{
		cfg: smbiosMemoryConfig{Command: "dmidecode", Timeout: time.Second},
		run: func(context.Context, string, ...string) ([]byte, error) {
			return []byte("Memory Device\n\tLocator: DIMM_A1\n\tSize: 16 GB\n\tSpeed: 3200 MT/s\nMemory Device\n\tLocator: DIMM_A2\n\tSize: No Module Installed\n"), nil
		},
	}
	if err := sm.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := sm.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Chart("smbios.memory.size.DIMM_A1"); !ok {
		t.Fatal("missing smbios chart")
	}
}

func TestDockerhubDCGMPANOSS3(t *testing.T) {
	dh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"name":"netdata","pull_count":10,"star_count":3,"status":"active","last_updated":"2020-01-01T00:00:00Z"}`))
	}))
	defer dh.Close()
	d := &dockerhubCollector{cfg: dockerhubConfig{URL: dh.URL, Repositories: []string{"netdata/netdata"}, Timeout: time.Second}}
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	if err := d.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := d.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}

	dc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("DCGM_FI_DEV_GPU_UTIL{gpu=\"0\"} 15\nDCGM_FI_DEV_FB_USED{gpu=\"0\"} 1024\nDCGM_FI_DEV_GPU_TEMP{gpu=\"0\"} 45\nDCGM_FI_DEV_POWER_USAGE{gpu=\"0\"} 30\n"))
	}))
	defer dc.Close()
	g := &dcgmCollector{cfg: dcgmConfig{URL: dc.URL, Timeout: time.Second}}
	if err := g.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := g.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}

	po := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<response status="success"><result><num-active>4</num-active><num-tcp>3</num-tcp><num-udp>1</num-udp><session-utilization>10</session-utilization></result></response>`))
	}))
	defer po.Close()
	p := &panosCollector{cfg: panosConfig{URL: po.URL, APIKey: "k", Timeout: time.Second}}
	if err := p.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := p.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}

	s3s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte("ok"))
	}))
	defer s3s.Close()
	s := &s3checkCollector{cfg: s3checkConfig{Buckets: []s3checkBucket{{Name: "b", URL: s3s.URL}}, Timeout: time.Second}}
	if err := s.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := s.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestPowerstorePowervaultScaleio(t *testing.T) {
	ps := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"c1","state":"Healthy","physical_used":100,"physical_total":1000,"healthy":1}`))
	}))
	defer ps.Close()
	p := &powerstoreCollector{cfg: powerstoreConfig{URL: ps.URL, Timeout: time.Second}}
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	if err := p.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := p.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}

	pv := &powervaultCollector{
		cfg: powervaultConfig{URL: "https://127.0.0.1", Timeout: time.Second},
		get: func(_ context.Context, path string) ([]byte, error) {
			if strings.Contains(path, "sensor") {
				return []byte(`{"sensors":[{"status":"OK"},{"status":"error"}]}`), nil
			}
			return []byte(`{"system":[{"health":"OK","ok":1}]}`), nil
		},
	}
	if err := pv.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := pv.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}

	sc := &scaleioCollector{
		cfg: scaleioConfig{URL: "https://127.0.0.1", Timeout: time.Second},
		get: func(context.Context, string) ([]byte, error) {
			return []byte(`{"id":"sys","maxCapacityInKb":1000,"capacityInUseInKb":200,"totalIosInProgress":5,"numOfSdcs":2,"numOfSds":3,"numOfVolumes":4}`), nil
		},
	}
	if err := sc.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := sc.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestM12GAutoDisable(t *testing.T) {
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	if err := (&ethtoolCollector{cfg: ethtoolConfig{Timeout: 50 * time.Millisecond}}).Init(reg); err == nil {
		t.Fatal("ethtool should disable without interfaces")
	}
	if err := (&dockerhubCollector{cfg: dockerhubConfig{Timeout: 50 * time.Millisecond}}).Init(reg); err == nil {
		t.Fatal("dockerhub should disable without repos")
	}
	if err := (&s3checkCollector{cfg: s3checkConfig{Timeout: 50 * time.Millisecond}}).Init(reg); err == nil {
		t.Fatal("s3check should disable without buckets")
	}
	if err := (&apCollector{cfg: apConfig{Command: "iw", Timeout: 50 * time.Millisecond}, run: func(context.Context, string, ...string) ([]byte, error) {
		return []byte("Interface wlan0\n\ttype managed\n"), nil
	}}).Init(reg); err == nil {
		t.Fatal("ap should disable without AP")
	}
	if err := (&smbiosMemoryCollector{cfg: smbiosMemoryConfig{Command: "dmidecode", Timeout: time.Second}, run: func(context.Context, string, ...string) ([]byte, error) {
		return []byte("Memory Device\n\tSize: No Module Installed\n"), nil
	}}).Init(reg); err == nil {
		t.Fatal("smbios should disable without modules")
	}
	if err := (&dcgmCollector{cfg: dcgmConfig{URL: "http://127.0.0.1:1", Timeout: 50 * time.Millisecond}}).Init(reg); err == nil {
		t.Fatal("dcgm should disable")
	}
}
