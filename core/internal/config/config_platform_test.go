package config

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestUnsetPlatformFieldsKeepEngineDefaults(t *testing.T) {
	c, err := Parse([]byte("mode: agent\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.DB.BlockSize != 0 || c.DB.Tier1BlockSize != 0 || c.DB.Tier2BlockSize != 0 {
		t.Fatalf("block sizes changed an existing file: %+v", c.DB)
	}
	if c.Plugins.MaxCharts != 0 || c.Plugins.MaxDimensions != 0 || c.Plugins.MaxLine != 0 || c.Plugins.RestartMin != 0 || c.Plugins.RestartMax != 0 {
		t.Fatalf("plugin limits changed an existing file: %+v", c.Plugins)
	}
	if c.Collectors.Timeout != 0 || !c.PluginsEnabled() {
		t.Fatalf("collector timeout or plugin enablement changed: timeout=%s enabled=%v", c.Collectors.Timeout, c.PluginsEnabled())
	}
}

func TestPlatformLimitsRejectOutOfRangeValues(t *testing.T) {
	for _, body := range []string{
		"db:\n  block_size: 10\n",
		"db:\n  block_size: 90000\n",
		"db:\n  tier1_block_size: 2\n",
		"db:\n  tier2_block_size: -1\n",
		"plugins:\n  max_charts: 10001\n",
		"plugins:\n  max_dimensions: 0\n  max_line: 100\n",
		"plugins:\n  restart_min: 1ms\n",
		"plugins:\n  restart_max: 48h\n",
		"plugins:\n  restart_min: 5s\n  restart_max: 1s\n",
		"plugins:\n  restart_max: 200ms\n",
		"collectors:\n  timeout: 2h\n",
		"collectors:\n  timeout: 1ms\n",
	} {
		if _, err := Parse([]byte(body)); err == nil {
			t.Fatalf("accepted %q", body)
		}
	}
}

func TestPlatformLimitsAcceptOperatorValues(t *testing.T) {
	c, err := Parse([]byte(`
db:
  block_size: 120
  tier1_block_size: 24
  tier2_block_size: 12
plugins:
  enabled: false
  max_charts: 32
  max_dimensions: 8
  max_labels: 16
  max_variables: 4
  max_line: 4096
  restart_min: 2s
  restart_max: 30s
collectors:
  timeout: 30s
  disabled: [diskspace]
`))
	if err != nil {
		t.Fatal(err)
	}
	if c.DB.BlockSize != 120 || c.DB.Tier1BlockSize != 24 || c.DB.Tier2BlockSize != 12 {
		t.Fatalf("db %+v", c.DB)
	}
	if c.PluginsEnabled() || c.Plugins.MaxCharts != 32 || c.Plugins.MaxDimensions != 8 || c.Plugins.MaxLabels != 16 || c.Plugins.MaxVariables != 4 || c.Plugins.MaxLine != 4096 {
		t.Fatalf("plugins %+v", c.Plugins)
	}
	if c.Plugins.RestartMin != 2*time.Second || c.Plugins.RestartMax != 30*time.Second {
		t.Fatalf("restart %s %s", c.Plugins.RestartMin, c.Plugins.RestartMax)
	}
	if c.Collectors.Timeout != 30*time.Second || len(c.Collectors.Disabled) != 1 {
		t.Fatalf("collectors timeout=%s disabled=%v", c.Collectors.Timeout, c.Collectors.Disabled)
	}
}

func TestExampleConfigDocumentsUsableDefaults(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	c, err := Load(filepath.Join(filepath.Dir(file), "..", "..", "..", "monitor.example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if c.DB.BlockSize != 900 || c.DB.Tier1BlockSize != 720 || c.DB.Tier2BlockSize != 360 {
		t.Fatalf("example block sizes = %d %d %d", c.DB.BlockSize, c.DB.Tier1BlockSize, c.DB.Tier2BlockSize)
	}
	if c.Plugins.MaxCharts != 128 || c.Plugins.MaxDimensions != 64 || c.Plugins.MaxLabels != 128 || c.Plugins.MaxVariables != 128 || c.Plugins.MaxLine != 65536 {
		t.Fatalf("example plugin budgets = %+v", c.Plugins)
	}
	if c.Plugins.RestartMin != time.Second || c.Plugins.RestartMax != time.Minute || c.Collectors.Timeout != 0 || !c.PluginsEnabled() {
		t.Fatalf("example timing plugins=%+v timeout=%s", c.Plugins, c.Collectors.Timeout)
	}
	if strings.TrimSpace(c.Mode) == "" {
		t.Fatal("example lost mode")
	}
}
