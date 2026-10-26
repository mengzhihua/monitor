// Package api serves the REST/WebSocket API and the embedded dashboard.
package api

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/mengzhihua/monitor/core/internal/audit"
	"github.com/mengzhihua/monitor/core/internal/collect"
	"github.com/mengzhihua/monitor/core/internal/health"
	"github.com/mengzhihua/monitor/core/internal/hostshell"
	"github.com/mengzhihua/monitor/core/internal/hub"
	"github.com/mengzhihua/monitor/core/internal/ingest"
	"github.com/mengzhihua/monitor/core/internal/operations"
	"github.com/mengzhihua/monitor/core/internal/registry"
	"github.com/mengzhihua/monitor/core/internal/sla"
	"github.com/mengzhihua/monitor/core/internal/stream"
	"github.com/mengzhihua/monitor/core/internal/tsdb"
)

type Options struct {
	Shell         hostshell.Config
	OperationsDir string // persistent operations, personal views and AI state; empty for ephemeral tests
	TicketWebhook string // POST handling JSON after a successful save; empty = off
	// ConfigPath is this process's monitor.yaml. Empty means the file API is read-only/unavailable.
	ConfigPath string
	// RequestRestart asks monitord to shut down and re-exec. Nil means restart is unavailable.
	RequestRestart func() error
	Version        string
	Commit         string // source commit SHA when known; shown in /api/v1/info
	BuildTime      string // UTC build / vcs.time stamp when known
	Dirty          bool   // true when the binary was built with a dirty worktree
	Mode           string // agent | hub (informational)
	StartedAt      time.Time
	AllowFrom      []string
	// Token is the legacy single admin credential; Users adds named
	// credentials with roles. With neither set the API is anonymous (admin).
	Token  string
	Users  []User
	Health *health.Engine // nil = alarms API disabled
	Logger *slog.Logger
	// Plugins is the injected extension runtime; HTTP does not own its workers.
	Plugins PluginRuntime
	// Nodes enables hub mode: /api/v1/stream ingestion and node= routing.
	Nodes *hub.Nodes
	// Stream reports this agent's own upstream connection in /api/v1/info.
	Stream *stream.Client
	// ExtraFunctions are hub-side functions (e.g. streaming) merged with collectors.
	ExtraFunctions []collect.Function
	// Cluster fans node lookups out to peer hubs.
	Cluster *hub.Cluster
	// Org is the Space/Room/claim store (hub mode).
	Org *hub.Org
	// Templates is the hub template + node-inventory store (hub mode).
	Templates *hub.Templates
	// PeerToken authenticates POST /api/v1/hub/ring and /api/v1/hub/vote from sibling hubs.
	PeerToken string
	// Elector chooses the active hub when peers are configured. Nil means this process notifies.
	Elector *hub.Elector
	// OIDC enables browser login against an identity provider.
	OIDC *OIDCConfig
	// LDAP enables POST /api/v1/auth/ldap.
	LDAP *LDAPConfig
	// Anomaly overrides the ML collector as the per-dimension bit source.
	Anomaly health.AnomalySource
	// Audit records mutating API calls and auth events; nil disables it.
	Audit *audit.Log
	// Services and Links feed the service tree, SLA report, and topology API.
	Services []sla.Service
	Links    []sla.Link
	// Commands are the only argv values POST /api/v1/commands may execute.
	Commands []Command
	// ReportEvery writes an availability CSV into ReportDir. Zero disables it.
	ReportEvery time.Duration
	ReportDir   string
	// SelfMonitor publishes notification-queue and hub node charts.
	SelfMonitor bool
	SAML        *SAMLConfig
	// TOTPSecret is the legacy admin token's second factor. Empty means off.
	TOTPSecret string
}

type Server struct {
	backgroundCancel   context.CancelFunc
	backgroundWG       sync.WaitGroup
	closeOnce          sync.Once
	shell              *shellState
	analysis           *aiState
	operationPeerSlots chan struct{}
	configMu           sync.Mutex
	operations         *operations.Store
	views              *operations.ViewStore
	reg                *registry.Registry
	db                 *tsdb.Store
	sched              *collect.Scheduler
	opt                Options
	log                *slog.Logger
	live               *liveHub
	nets               []*net.IPNet
	mux                *http.ServeMux
	ingest             *ingest.Mapper
	otlp               *ingest.Mapper
	oidc               *oidcState
	ldap               *LDAPConfig
	shares             *shareStore
	audit              *audit.Log
	saml               *samlState
}

func New(reg *registry.Registry, db *tsdb.Store, sched *collect.Scheduler, opt Options) (*Server, error) {
	if opt.Logger == nil {
		opt.Logger = slog.Default()
	}
	s := &Server{reg: reg, db: db, sched: sched, opt: opt, log: opt.Logger, mux: http.NewServeMux(),
		operationPeerSlots: make(chan struct{}, operationPeerConcurrency),
		ingest:             ingest.NewMapper(ingest.Options{Prefix: "om", Plugin: "ingest", Module: "openmetrics", Family: "openmetrics"}),
		otlp:               ingest.NewMapper(ingest.Options{Prefix: "otlp", Plugin: "ingest", Module: "otlp", Family: "otlp"})}
	for _, c := range opt.AllowFrom {
		if !strings.Contains(c, "/") {
			if strings.Contains(c, ":") {
				c += "/128"
			} else {
				c += "/32"
			}
		}
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			return nil, fmt.Errorf("web.allow_from %q: %w", c, err)
		}
		s.nets = append(s.nets, n)
	}
	if err := validateUsers(opt.Users); err != nil {
		return nil, err
	}
	s.oidc = newOIDC(opt.OIDC)
	if s.oidc != nil {
		switch Role(s.oidc.cfg.Role) {
		case RoleAdmin, RoleViewer, RoleTroubleshooter:
		default:
			return nil, fmt.Errorf("invalid OIDC role %q", s.oidc.cfg.Role)
		}
	}
	s.ldap = opt.LDAP
	if s.ldap != nil && s.ldap.URL == "" && s.ldap.Bind == nil {
		s.ldap = nil
	}
	if s.ldap != nil && s.ldap.Role != "" {
		switch Role(s.ldap.Role) {
		case RoleAdmin, RoleViewer, RoleTroubleshooter:
		default:
			return nil, fmt.Errorf("invalid LDAP role %q", s.ldap.Role)
		}
	}
	s.shares = newShareStore()
	s.audit = opt.Audit
	var err error
	s.saml, err = newSAML(opt.SAML)
	if err != nil {
		return nil, err
	}
	s.operations, err = operations.Open(opt.OperationsDir)
	if err != nil {
		return nil, fmt.Errorf("open operations store: %w", err)
	}
	s.views, err = operations.OpenViews(opt.OperationsDir)
	if err != nil {
		return nil, fmt.Errorf("open personal views store: %w", err)
	}
	s.analysis, err = newAIState(opt.OperationsDir)
	if err != nil {
		return nil, fmt.Errorf("open AI state: %w", err)
	}
	if err := opt.Shell.Validate(); err != nil {
		return nil, err
	}
	s.shell = newShellState()
	s.live = newLiveHub(reg, s.log)
	s.routes()
	s.startBackground()
	return s, nil
}

// PublishNodeSample / PublishNodeAlarm feed a remote node's activity into
// the live WebSocket; wire them into hub.Options.
func (s *Server) PublishNodeSample(nodeID, chartID string, ts int64, values map[string]float64) {
	s.live.broadcastNode(nodeID, chartID, ts, values)
}

func (s *Server) PublishNodeAlarm(nodeID string, e health.LogEntry) {
	s.publishAlarm(nodeID, e)
}

func (s *Server) Handler() http.Handler {
	return s.guard(s.mux)
}
