package main

import (
	"log/slog"
	"path/filepath"
	"time"

	"github.com/mengzhihua/monitor/core/internal/api"
	"github.com/mengzhihua/monitor/core/internal/audit"
	"github.com/mengzhihua/monitor/core/internal/config"
	"github.com/mengzhihua/monitor/core/internal/health"
	"github.com/mengzhihua/monitor/core/internal/plugins"
	"github.com/mengzhihua/monitor/core/internal/stream"
)

func serverOptions(cfg *config.Config, cfgPath string, eng *health.Engine, pm *plugins.Manager, sc *stream.Client, auditLog *audit.Log, log *slog.Logger) api.Options {
	var users []api.User
	for _, u := range cfg.Web.Users {
		users = append(users, api.User{Name: u.Name, Token: u.Token, Role: api.Role(u.Role), Rooms: config.EffectiveRooms(u, cfg.Web.Groups), HostGroups: config.EffectiveHostGroups(u, cfg.Web.Groups), TOTP: u.TOTP, ExpiresAt: u.ExpiresAt})
	}

	apiOpt := api.Options{
		Shell:         cfg.Shell,
		Audit:         auditLog,
		OperationsDir: filepath.Join(cfg.Global.DataDir, "operations"),
		TicketWebhook: cfg.Web.TicketWebhook,
		Version:       version,
		Commit:        commit,
		BuildTime:     buildTime,
		Dirty:         dirty == "true",
		Mode:          cfg.Mode,
		StartedAt:     time.Now(),
		AllowFrom:     cfg.Web.AllowFrom,
		Token:         cfg.Web.Token,
		TOTPSecret:    cfg.Web.TOTPSecret,
		Users:         users,
		Services:      cfg.Services,
		Links:         cfg.Topology,
		Commands:      apiCommands(cfg.Commands),
		ReportEvery:   cfg.Reports.Every,
		ReportDir:     reportDir(cfg),
		SelfMonitor:   true,
		Health:        eng,
		Stream:        sc,
		ConfigPath:    cfgPath,
		Logger:        log.With("component", "api"),
	}
	if pm != nil {
		apiOpt.Plugins = pm
	}
	if cfg.Web.OIDC.Issuer != "" || cfg.Web.OIDC.ClientID != "" {
		apiOpt.OIDC = &api.OIDCConfig{Issuer: cfg.Web.OIDC.Issuer, ClientID: cfg.Web.OIDC.ClientID,
			ClientSecret: cfg.Web.OIDC.ClientSecret, RedirectURL: cfg.Web.OIDC.RedirectURL, Role: cfg.Web.OIDC.Role}
	}
	if cfg.Web.SAML.SSOURL != "" {
		apiOpt.SAML = &api.SAMLConfig{EntityID: cfg.Web.SAML.EntityID, ACSURL: cfg.Web.SAML.ACSURL,
			SSOURL: cfg.Web.SAML.SSOURL, CertPEM: cfg.Web.SAML.CertPEM, Role: cfg.Web.SAML.Role}
	}
	if cfg.Web.LDAP.URL != "" {
		apiOpt.LDAP = &api.LDAPConfig{URL: cfg.Web.LDAP.URL, UserDN: cfg.Web.LDAP.UserDN,
			BindDN: cfg.Web.LDAP.BindDN, BindPass: cfg.Web.LDAP.BindPass, Role: cfg.Web.LDAP.Role}
	}
	return apiOpt
}

func apiCommands(in []config.Command) []api.Command {
	out := make([]api.Command, 0, len(in))
	for _, c := range in {
		out = append(out, api.Command{Name: c.Name, Argv: c.Argv, Timeout: c.Timeout})
	}
	return out
}

func reportDir(cfg *config.Config) string {
	if cfg.Reports.Every <= 0 {
		return ""
	}
	dir := cfg.Reports.Dir
	if dir == "" {
		dir = filepath.Join(cfg.Global.DataDir, "reports")
	}
	return dir
}
