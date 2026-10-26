package api

import (
	"net/http"

	"github.com/mengzhihua/monitor/core/internal/stream"
)

// routes is the composition root for HTTP endpoints. Each registration group
// owns one API domain; Handler applies the common security boundary once.
func (s *Server) routes() {
	m := s.mux
	s.registerShellRoutes(m)
	s.registerOperationsRoutes(m)
	s.registerMetricsRoutes(m)
	s.registerAlertsRoutes(m)
	s.registerCollectorsRoutes(m)
	s.registerIngestRoutes(m)
	s.registerHubRoutes(m)
	s.registerAuthRoutes(m)
	s.registerManagementRoutes(m)
	s.registerPluginRoutes(m)
	if s.opt.Nodes != nil {
		m.HandleFunc("GET "+stream.Path, s.opt.Nodes.HandleStream)
		m.HandleFunc("GET "+stream.PathACLK, s.opt.Nodes.HandleACLK)
	}
	m.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
		s.writePrometheus(w, userOf(r))
	})
	m.HandleFunc("GET /api/v1/debug/pprof/heap", s.handleDebugHeap)
	m.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok\n")) })
	m.Handle("/", s.uiHandler())
}

func (s *Server) registerShellRoutes(m *http.ServeMux) {
	m.HandleFunc("GET /api/v1/shell/targets", s.handleShellTargets)
	m.HandleFunc("POST /api/v1/shell/tickets", s.handleShellTicket)
	m.HandleFunc("GET /api/v1/shell/connect", s.handleShellConnect)
	m.HandleFunc("GET /api/v1/shell/sessions", s.handleShellSessions)
	m.HandleFunc("DELETE /api/v1/shell/sessions/{id}", s.handleShellRevoke)
}

func (s *Server) registerOperationsRoutes(m *http.ServeMux) {
	m.HandleFunc("GET /api/v1/ai/settings", s.handleAISettings)
	m.HandleFunc("PUT /api/v1/ai/settings", s.handleAISettings)
	m.HandleFunc("DELETE /api/v1/ai/settings", s.handleAISettings)
	m.HandleFunc("POST /api/v1/ai/preview", s.handleAIPreview)
	m.HandleFunc("POST /api/v1/ai/analyze", s.handleAIAnalyze)
	m.HandleFunc("GET /api/v1/ai/resolutions", s.handleAIResolutions)
	m.HandleFunc("POST /api/v1/ai/resolutions", s.handleAIResolutions)
	m.HandleFunc("DELETE /api/v1/ai/resolutions", s.handleAIResolutions)
	m.HandleFunc("POST /api/v1/ai/resolutions/review", s.handleAIResolutionReview)
	m.HandleFunc("GET /api/v1/operations", s.handleOperations)
	m.HandleFunc("GET /api/v1/operations/notifications", s.handleNotificationDiagnostics)
	m.HandleFunc("POST /api/v1/operations/notifications/test", s.handleNotificationTest)
	m.HandleFunc("GET /api/v1/operations/maintenance", s.handleMaintenance)
	m.HandleFunc("POST /api/v1/operations/maintenance", s.handleMaintenance)
	m.HandleFunc("GET /api/v1/operations/views", s.handleOperationsViews)
	m.HandleFunc("POST /api/v1/operations/views", s.handleOperationsViews)
	m.HandleFunc("GET /api/v1/operations/history", s.handleOperationsHistory)
	m.HandleFunc("GET /api/v1/operations/history/export", s.handleOperationsHistoryExport)
	m.HandleFunc("GET /api/v1/operations/diagnostic/export", s.handleDiagnosticExport)
	m.HandleFunc("POST /api/v1/operations/acknowledgements", s.handleAcknowledgement)
	m.HandleFunc("POST /api/v1/operations/handling", s.handleHandling)
	m.HandleFunc("POST /api/v1/operations/handling/batch", s.handleHandlingBatch)
	m.HandleFunc("POST /api/v1/notify/push", s.handlePushTest)
}

func (s *Server) registerMetricsRoutes(m *http.ServeMux) {
	m.HandleFunc("GET /api/v1/info", s.handleInfo)
	m.HandleFunc("GET /api/v1/charts", s.handleCharts)
	m.HandleFunc("GET /api/v1/chart", s.handleChart)
	m.HandleFunc("GET /api/v1/data", s.handleData)
	m.HandleFunc("GET /api/v1/allmetrics", s.handleAllMetrics)
	m.HandleFunc("GET /api/v1/badge.svg", s.handleBadge)
	m.HandleFunc("GET /api/v2/data", s.handleDataV2)
	m.HandleFunc("GET /api/v2/q", s.handleDataV2)
	m.HandleFunc("GET /api/v2/contexts", s.handleContextsV2)
	m.HandleFunc("GET /api/v2/badge.svg", s.handleBadge)
	m.HandleFunc("GET /api/v3/info", s.handleInfoV3)
	m.HandleFunc("GET /api/v3/data", s.handleDataV3)
	m.HandleFunc("GET /api/v3/q", s.handleDataV3)
	m.HandleFunc("GET /api/v3/contexts", s.handleContextsV3)
	m.HandleFunc("GET /api/v3/context", s.handleContext)
	m.HandleFunc("GET /api/v3/weights", s.handleWeights)
	m.HandleFunc("GET /api/v3/badge.svg", s.handleBadge)
	m.HandleFunc("GET /api/v3/allmetrics", s.handleAllMetrics)
	m.HandleFunc("GET /api/v1/live", s.handleLive)
	m.HandleFunc("GET /api/v1/contexts", s.handleContexts)
	m.HandleFunc("GET /api/v1/weights", s.handleWeights)
}

func (s *Server) registerAlertsRoutes(m *http.ServeMux) {
	m.HandleFunc("GET /api/v1/alarm_count", s.handleAlarmCount)
	m.HandleFunc("GET /api/v2/alert_transitions", s.handleAlertTransitions)
	m.HandleFunc("GET /api/v1/alarm_transitions", s.handleAlertTransitions)
	m.HandleFunc("GET /api/v3/alerts", s.handleAlarms)
	m.HandleFunc("GET /api/v3/alert_transitions", s.handleAlertTransitionsV3)
	m.HandleFunc("GET /api/v3/alert_config", s.handleAlertConfig)
	m.HandleFunc("PUT /api/v3/alert_config", s.handleAlertConfig)
	m.HandleFunc("POST /api/v3/alert_config", s.handleAlertConfig)
	m.HandleFunc("DELETE /api/v3/alert_config", s.handleAlertConfig)
	m.HandleFunc("GET /api/v1/alert_config", s.handleAlertConfig)
	m.HandleFunc("PUT /api/v1/alert_config", s.handleAlertConfig)
	m.HandleFunc("POST /api/v1/alert_config", s.handleAlertConfig)
	m.HandleFunc("DELETE /api/v1/alert_config", s.handleAlertConfig)
	m.HandleFunc("POST /api/v1/manage/alert-rules/preview", s.handlePreviewAlertRule)
	m.HandleFunc("POST /api/v1/manage/alert-rules", s.handleManagedAlertRules)
	m.HandleFunc("GET /api/v1/alarms", s.handleAlarms)
	m.HandleFunc("GET /api/v1/alarm_log", s.handleAlarmLog)
	m.HandleFunc("GET /api/v1/alarm_rules", s.handleAlarmRules)
	m.HandleFunc("GET /api/v1/alarm_variables", s.handleAlarmVariables)
	m.HandleFunc("GET /api/v1/alarms/silence", s.handleSilence)
	m.HandleFunc("POST /api/v1/alarms/silence", s.handleSilence)
	m.HandleFunc("POST /api/v1/alarms/close", s.handleAlarmClose)
	m.HandleFunc("GET /api/v1/alarm_summary", s.handleAlarmSummary)
}

func (s *Server) registerCollectorsRoutes(m *http.ServeMux) {
	m.HandleFunc("GET /api/v3/functions", s.handleFunctions)
	m.HandleFunc("GET /api/v3/function", s.handleFunction)
	m.HandleFunc("GET /api/v1/collectors", s.handleCollectors)
	m.HandleFunc("GET /api/v1/prometheus/catalog", s.handlePrometheusCatalog)
	m.HandleFunc("GET /api/v1/functions", s.handleFunctions)
	m.HandleFunc("GET /api/v1/function", s.handleFunction)
	m.HandleFunc("GET /api/v1/logs", s.handleLogs)
}

func (s *Server) registerIngestRoutes(m *http.ServeMux) {
	m.HandleFunc("POST /api/v1/ingest/openmetrics", s.handleIngestOpenMetrics)
	m.HandleFunc("PUT /api/v1/ingest/openmetrics", s.handleIngestOpenMetrics)
	m.HandleFunc("POST /api/v1/ingest/otlp", s.handleIngestOTLP)
	m.HandleFunc("POST /v1/metrics", s.handleIngestOTLP)
}

func (s *Server) registerHubRoutes(m *http.ServeMux) {
	m.HandleFunc("GET /api/v2/nodes", s.handleNodesV2)
	m.HandleFunc("GET /api/v3/nodes", s.handleNodesV3)
	m.HandleFunc("GET /api/v1/nodes", s.handleNodes)
	m.HandleFunc("DELETE /api/v1/nodes", s.handleForgetNode)
	m.HandleFunc("GET /api/v1/hub/spaces", s.handleSpaces)
	m.HandleFunc("POST /api/v1/hub/spaces", s.handleSpaces)
	m.HandleFunc("PUT /api/v1/hub/spaces", s.handleSpaces)
	m.HandleFunc("DELETE /api/v1/hub/spaces", s.handleSpaces)
	m.HandleFunc("GET /api/v1/hub/host-groups", s.handleHostGroups)
	m.HandleFunc("POST /api/v1/hub/host-groups", s.handleHostGroups)
	m.HandleFunc("PUT /api/v1/hub/host-groups", s.handleHostGroups)
	m.HandleFunc("DELETE /api/v1/hub/host-groups", s.handleHostGroups)
	m.HandleFunc("GET /api/v1/hub/proxies", s.handleProxies)
	m.HandleFunc("POST /api/v1/hub/proxies", s.handleProxies)
	m.HandleFunc("PUT /api/v1/hub/proxies", s.handleProxies)
	m.HandleFunc("DELETE /api/v1/hub/proxies", s.handleProxies)
	m.HandleFunc("POST /api/v1/hub/proxies/heartbeat", s.handleProxyHeartbeat)
	m.HandleFunc("GET /api/v1/hub/leader", s.handleLeader)
	m.HandleFunc("POST /api/v1/hub/vote", s.handleVote)
	m.HandleFunc("GET /api/v1/hub/rooms", s.handleRooms)
	m.HandleFunc("POST /api/v1/hub/rooms", s.handleRooms)
	m.HandleFunc("DELETE /api/v1/hub/rooms", s.handleRooms)
	m.HandleFunc("PUT /api/v1/hub/rooms", s.handleRooms)
	m.HandleFunc("GET /api/v1/hub/claim-tokens", s.handleClaimTokens)
	m.HandleFunc("POST /api/v1/hub/claim-tokens", s.handleClaimTokens)
	m.HandleFunc("POST /api/v1/claim", s.handleClaimRedeem)
	m.HandleFunc("GET /api/v1/hub/config", s.handleHubConfig)
	m.HandleFunc("PUT /api/v1/hub/config", s.handleHubConfig)
	m.HandleFunc("GET /api/v1/agent/config", s.handleAgentConfig)
	m.HandleFunc("GET /api/v1/hub/templates", s.handleTemplates)
	m.HandleFunc("PUT /api/v1/hub/templates", s.handleTemplates)
	m.HandleFunc("DELETE /api/v1/hub/templates", s.handleTemplates)
	m.HandleFunc("GET /api/v1/hub/templates/effective", s.handleTemplateEffective)
	m.HandleFunc("GET /api/v1/hub/inventory", s.handleInventory)
	m.HandleFunc("PUT /api/v1/hub/inventory", s.handleInventory)
	m.HandleFunc("DELETE /api/v1/hub/inventory", s.handleInventory)
	m.HandleFunc("POST /api/v1/hub/ring", s.handleRing)
	m.HandleFunc("GET /api/v1/hub/console", s.handleConsole)
}

func (s *Server) registerAuthRoutes(m *http.ServeMux) {
	m.HandleFunc("GET /api/v1/auth/oidc/status", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, map[string]bool{"enabled": s.oidc != nil}) })
	m.HandleFunc("POST /api/v1/auth/oidc/logout", s.handleOIDCLogout)
	m.HandleFunc("GET /api/v1/auth/oidc/login", s.handleOIDCLogin)
	m.HandleFunc("GET /api/v1/auth/oidc/callback", s.handleOIDCCallback)
	m.HandleFunc("POST /api/v1/auth/ldap", s.handleLDAP)
	m.HandleFunc("GET /api/v1/auth/saml/login", s.handleSAMLLogin)
	m.HandleFunc("POST /api/v1/auth/saml/acs", s.handleSAMLACS)
}

func (s *Server) registerManagementRoutes(m *http.ServeMux) {
	m.HandleFunc("GET /api/v1/audit", s.handleAudit)
	m.HandleFunc("GET /api/v1/manage/health", s.handleManageHealth)
	m.HandleFunc("PUT /api/v1/manage/health", s.handleManageHealth)
	m.HandleFunc("GET /api/v1/manage/config", s.handleManageConfig)
	m.HandleFunc("PUT /api/v1/manage/config", s.handleManageConfig)
	m.HandleFunc("POST /api/v1/manage/config/rollback", s.handleManageConfigRollback)
	m.HandleFunc("POST /api/v1/manage/restart", s.handleManageRestart)
	m.HandleFunc("GET /api/v1/checks", s.handleChecks)
	m.HandleFunc("POST /api/v1/checks", s.handleChecks)
	m.HandleFunc("GET /api/v1/services", s.handleServices)
	m.HandleFunc("GET /api/v1/reports/availability", s.handleAvailability)
	m.HandleFunc("GET /api/v1/topology", s.handleTopology)
	m.HandleFunc("POST /api/v1/commands", s.handleCommand)
	m.HandleFunc("POST /api/v1/discovery/scan", s.handleDiscovery)
	m.HandleFunc("POST /api/v1/share", s.handleShare)
	m.HandleFunc("GET /api/v1/share", s.handleShare)
}
