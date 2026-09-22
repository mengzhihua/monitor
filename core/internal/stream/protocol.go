// Package stream implements agent → hub streaming: an agent opens an
// outbound WebSocket to the hub and pushes chart definitions, live samples
// and alarm transitions; the hub replies with what it already holds so the
// agent can replicate the gap from its local TSDB, and may call the agent's
// functions over the same connection.
//
// Frames are JSON objects with a "type" discriminator (one frame per
// WebSocket text message):
//
//	agent → hub: hello, chart, chart_del, data, alarm, alarms, func_result, query_result
//	hub → agent: welcome, func_call, query, config, error, ping
package stream

import (
	"encoding/json"

	"github.com/mengzhihua/monitor/core/internal/health"
	"github.com/mengzhihua/monitor/core/internal/registry"
)

const (
	// Path is the JSON-frame hub endpoint agents connect to.
	Path = "/api/v1/stream"
	// PathACLK is MQTT-over-WebSocket (Netdata ACLK semantics) on the same Frame payloads.
	PathACLK = "/api/v1/aclk"

	TypeHello       = "hello"
	TypeWelcome     = "welcome"
	TypeChart       = "chart"
	TypeChartDel    = "chart_del"
	TypeData        = "data"
	TypeAlarm       = "alarm"
	TypeAlarms      = "alarms" // full snapshot of the agent's current alarm state
	TypeFuncCall    = "func_call"
	TypeFuncResult  = "func_result"
	TypeQuery       = "query"        // hub → agent: live metric query (Cloud proxy storage)
	TypeQueryResult = "query_result" // agent → hub
	TypeConfig      = "config"       // hub → agent: disabled collectors overlay
	TypeError       = "error"
)

// ACLKCapabilities is advertised in hello / info.aclk.
var ACLKCapabilities = []string{"stream", "functions", "alarms", "config", "query"}

// Frame is the union of every message; only the fields relevant to Type are
// populated.
type Frame struct {
	Type string `json:"type"`

	// hello
	Host         *registry.Host `json:"host,omitempty"`
	Version      string         `json:"version,omitempty"`
	Functions    []FunctionInfo `json:"functions,omitempty"`
	Capabilities []string       `json:"capabilities,omitempty"`
	Protocol     string         `json:"protocol,omitempty"` // "stream" | "mqtt"
	Claimed      bool           `json:"claimed,omitempty"`

	// config (hub → agent)
	Disabled []string `json:"disabled,omitempty"`

	// welcome: last sample time the hub holds per chart, so the agent can
	// replicate only what is missing. ReplicateFrom bounds how far back.
	Last          map[string]int64 `json:"last,omitempty"`
	ReplicateFrom int64            `json:"replicate_from,omitempty"`

	// chart / chart_del
	Chart *ChartDef `json:"chart_def,omitempty"`
	ID    string    `json:"id,omitempty"`

	// data: one collection of one chart. Replicated (historical) samples set
	// Replay so the hub does not treat them as fresh liveness.
	ChartID string             `json:"chart,omitempty"`
	T       int64              `json:"t,omitempty"`
	V       map[string]float64 `json:"v,omitempty"`
	Replay  bool               `json:"replay,omitempty"`

	// alarm (one transition) / alarms (snapshot sent after connect so state
	// changes that happened while disconnected are not lost)
	Alarm  *health.LogEntry  `json:"alarm,omitempty"`
	Alarms []health.LogEntry `json:"alarms,omitempty"`

	// func_call / func_result
	CallID uint64            `json:"call_id,omitempty"`
	Name   string            `json:"name,omitempty"`
	Args   map[string]string `json:"args,omitempty"`
	Result json.RawMessage   `json:"result,omitempty"`
	Error  string            `json:"error,omitempty"`
}

// SnapshotEntry renders the current state of an alarm as a log entry so the
// hub can mirror it with the same code path as live transitions.
func SnapshotEntry(a health.Alarm, hostname string) health.LogEntry {
	return health.LogEntry{AlarmID: a.ID, When: a.LastStatusChange, Updated: a.LastUpdated, Hostname: hostname, Name: a.Name, Chart: a.Chart,
		Context: a.Context, Family: a.Family, Class: a.Class, Type: a.Type, Component: a.Component,
		Status: a.Status, OldStatus: a.Status, Value: a.Value, OldValue: a.Value, Units: a.Units, Info: a.Info, Recipient: a.Recipient}
}

// FunctionInfo advertises an agent function to the hub.
type FunctionInfo struct {
	Name    string `json:"name"`
	Help    string `json:"help"`
	Timeout int    `json:"timeout"`
}

// ChartDef is the wire form of a chart. Values on the wire are already
// post-algorithm, so dimensions carry no algorithm/multiplier/divisor.
type ChartDef struct {
	ID          string            `json:"id"`
	Context     string            `json:"context"`
	Family      string            `json:"family"`
	Title       string            `json:"title"`
	Units       string            `json:"units"`
	Type        string            `json:"chart_type"`
	Priority    int               `json:"priority"`
	UpdateEvery int               `json:"update_every"`
	Plugin      string            `json:"plugin,omitempty"`
	Module      string            `json:"module,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
	Dimensions  []DimDef          `json:"dimensions"`
}

type DimDef struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Hidden bool   `json:"hidden,omitempty"`
}

// DefOf snapshots a registry chart for the wire.
func DefOf(c *registry.Chart) *ChartDef {
	d := &ChartDef{ID: c.ID, Context: c.Context, Family: c.Family, Title: c.Title, Units: c.Units,
		Type: string(c.Type), Priority: c.Priority, UpdateEvery: c.UpdateEvery, Plugin: c.Plugin, Module: c.Module, Labels: c.Labels}
	for _, dim := range c.Dims() {
		d.Dimensions = append(d.Dimensions, DimDef{ID: dim.ID, Name: dim.Name, Hidden: dim.Hidden})
	}
	return d
}

// ToChart builds a registry chart (absolute dimensions) from a definition.
func (d *ChartDef) ToChart() *registry.Chart {
	c := &registry.Chart{ID: d.ID, Context: d.Context, Family: d.Family, Title: d.Title, Units: d.Units,
		Type: registry.ChartType(d.Type), Priority: d.Priority, UpdateEvery: d.UpdateEvery, Plugin: d.Plugin, Module: d.Module, Labels: d.Labels}
	for _, dim := range d.Dimensions {
		c.Dimensions = append(c.Dimensions, &registry.Dimension{ID: dim.ID, Name: dim.Name, Hidden: dim.Hidden, Algorithm: registry.Absolute})
	}
	return c
}

// Fingerprint identifies a definition's shape so an agent knows when to
// resend it (new dimensions, renamed title...).
func (d *ChartDef) Fingerprint() string {
	b, _ := json.Marshal(d)
	return string(b)
}
