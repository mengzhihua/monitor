package main

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/mengzhihua/monitor/core/internal/api"
	"github.com/mengzhihua/monitor/core/internal/collect"
	"github.com/mengzhihua/monitor/core/internal/config"
	"github.com/mengzhihua/monitor/core/internal/health"
	"github.com/mengzhihua/monitor/core/internal/hub"
	"github.com/mengzhihua/monitor/core/internal/stream"
	"github.com/mengzhihua/monitor/core/internal/tsdb"
)

// configureHub builds the optional Hub and attaches its API capabilities.
// The callbacks run only after the caller assigns srv and starts nodes.Run.
func configureHub(cfg *config.Config, db *tsdb.Store, apiOpt *api.Options, srv **api.Server, log *slog.Logger) (*hub.Nodes, *hub.Cluster, error) {
	var err error
	var nodes *hub.Nodes
	var cluster *hub.Cluster
	var org *hub.Org
	var srvTemplates *hub.Templates
	if cfg.Mode == "hub" {
		hubDir := filepath.Join(cfg.Global.DataDir, "hub")
		org, err = hub.OpenOrg(hubDir)
		if err != nil {
			return nil, nil, fmt.Errorf("hub org: %w", err)
		}
		templates, err := hub.OpenTemplates(hubDir)
		if err != nil {
			return nil, nil, fmt.Errorf("hub templates: %w", err)
		}
		srvTemplates = templates
		if len(org.Spaces()) == 0 {
			spName, rmName := cfg.Hub.Space, cfg.Hub.Room
			if spName == "" {
				spName = "default"
			}
			if rmName == "" {
				rmName = "default"
			}
			if sp, err := org.CreateSpace(spName); err == nil {
				_, _ = org.CreateRoom(sp.ID, rmName)
			}
		}
		if len(cfg.Hub.APIKeys) == 0 && len(org.Keys()) == 0 {
			log.Warn("hub mode without hub.api_keys: agents cannot connect until a claim token is redeemed")
		}
		nodes, err = hub.Open(db, hubDir, hub.Options{
			Keys:              cfg.Hub.APIKeys,
			PSK:               cfg.Hub.PSK,
			RequireClientCert: cfg.Hub.RequireClientCert,
			ExtraKeys:         org.Keys,
			Replicate:         cfg.Hub.Replicate,
			MaxNodes:          cfg.Hub.MaxNodes,
			MaxChartsPerNode:  cfg.Hub.MaxChartsPerNode,
			MaxDimsPerChart:   cfg.Hub.MaxDimsPerChart,
			Storage:           cfg.Hub.Storage,
			NodeConfig: func(nodeID string) *hub.NodeConfig {
				c, ok := org.GetConfig(nodeID)
				if !ok {
					return nil
				}
				return &c
			},
			HealthOverlay: func(nodeID string) *stream.HealthOverlay {
				_, roomID := org.Membership(nodeID)
				var labels map[string]string
				if nd, ok := nodes.Get(nodeID); ok {
					labels = nd.Host.Labels
				}
				ov := templates.Effective(nodeID, roomID, labels)
				return &ov // always non-nil when templates are enabled: an empty
				// overlay (Rev 0) tells the agent to clear stale template rules
			}, // resolved per-node template overlay
			OnConfigState: func(nodeID string, f stream.Frame) {
				if f.ConfigYAML != "" {
					if err := org.SetReport(nodeID, f.ConfigYAML, time.Now().Unix()); err != nil {
						log.Warn("hub: save agent config report", "node", nodeID, "err", err)
					}
				}
				if f.ApplyState != "" {
					st := hub.ApplyState{Rev: f.ConfigRev, State: f.ApplyState, Error: f.ApplyError, At: time.Now().Unix()}
					if err := org.SetApply(nodeID, st); err != nil {
						log.Warn("hub: save agent apply state", "node", nodeID, "err", err)
					}
					if f.ApplyState == "rejected" {
						log.Warn("hub: agent rejected config", "node", nodeID, "rev", f.ConfigRev, "reason", f.ApplyError)
					}
				}
			},
			Logger:   log.With("component", "hub"),
			OnSample: func(n, c string, t int64, v map[string]float64) { (*srv).PublishNodeSample(n, c, t, v) },
			OnAlarm:  func(n string, e health.LogEntry) { (*srv).PublishNodeAlarm(n, e) },
		})
		if err != nil {
			return nil, nil, fmt.Errorf("hub: %w", err)
		}
		apiOpt.Nodes = nodes
		apiOpt.Org = org
		apiOpt.Templates = srvTemplates
		apiOpt.PeerToken = cfg.Hub.PeerToken
		apiOpt.ExtraFunctions = []collect.Function{streamingFunction(nodes)}
		if len(cfg.Hub.Peers) > 0 {
			cluster = hub.NewCluster(cfg.Hub.Peers, cfg.Hub.PeerToken, log.With("component", "cluster"))
			cluster.SetNodes(nodes)
			apiOpt.Cluster = cluster
			id := cfg.Global.Hostname
			if id == "" {
				id = "hub"
			}
			apiOpt.Elector = hub.NewElector(id, cfg.Hub.Peers, cfg.Hub.PeerToken)
		}
	}
	return nodes, cluster, nil
}

func streamingFunction(nodes *hub.Nodes) collect.Function {
	return collect.Function{
		Name:    "streaming",
		Help:    "Hub node connection status (live/stale/offline)",
		Timeout: 5,
		Run: func(_ context.Context, _ map[string]string) (any, error) {
			now := time.Now()
			list := nodes.List()
			type row struct {
				ID       string `json:"id"`
				Hostname string `json:"hostname"`
				Status   string `json:"status"`
				LastData int64  `json:"last_data"`
				Charts   int    `json:"charts"`
			}
			out := collect.Table{Columns: []string{"id", "hostname", "status", "last_data", "charts"}, Total: len(list)}
			out.Rows = make([]any, len(list))
			for i, n := range list {
				inf := n.Info(now)
				out.Rows[i] = row{ID: inf.ID, Hostname: inf.Hostname, Status: inf.Status, LastData: inf.LastData, Charts: inf.ChartsCount}
			}
			return out, nil
		},
	}
}
