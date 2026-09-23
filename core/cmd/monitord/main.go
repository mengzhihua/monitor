// monitord is the Monitor agent: it collects host metrics every second, stores
// them in the embedded TSDB and serves the API + dashboard over HTTP.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/mem"

	"github.com/mengzhihua/monitor/core/internal/api"
	"github.com/mengzhihua/monitor/core/internal/backup"
	"github.com/mengzhihua/monitor/core/internal/collect"
	"github.com/mengzhihua/monitor/core/internal/config"
	"github.com/mengzhihua/monitor/core/internal/discover"
	"github.com/mengzhihua/monitor/core/internal/export"
	"github.com/mengzhihua/monitor/core/internal/health"
	"github.com/mengzhihua/monitor/core/internal/hub"
	"github.com/mengzhihua/monitor/core/internal/plugins"
	"github.com/mengzhihua/monitor/core/internal/registry"
	"github.com/mengzhihua/monitor/core/internal/stream"
	"github.com/mengzhihua/monitor/core/internal/tsdb"
)

var version = "2.0.1" // release builds override via -ldflags "-X main.version=..."

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "monitord:", err)
		os.Exit(1)
	}
}

func run() error {
	cfgPath := flag.String("config", "monitor.yaml", "path to config file")
	listen := flag.String("listen", "", "override web.listen (e.g. :19999)")
	dataDir := flag.String("data-dir", "", "override global.data_dir")
	logLevel := flag.String("log-level", "info", "debug|info|warn|error")
	listCollectors := flag.Bool("list-collectors", false, "print registered collector names as JSON and exit")
	showVersion := flag.Bool("version", false, "print version and exit")
	backupDir := flag.String("backup-dir", "", "offline backup to a new directory and exit")
	restoreFrom := flag.String("restore-from", "", "restore backup into a new data directory and exit")
	flag.Parse()

	if *listCollectors {
		return json.NewEncoder(os.Stdout).Encode(collect.Available())
	}
	if *showVersion {
		fmt.Printf("monitord %s %s/%s\n", version, runtime.GOOS, runtime.GOARCH)
		return nil
	}

	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(*logLevel)); err != nil {
		return fmt.Errorf("bad -log-level: %w", err)
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl}))
	slog.SetDefault(log)

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	if *listen != "" {
		cfg.Web.Listen = *listen
	}
	if *dataDir != "" {
		cfg.Global.DataDir = *dataDir
	}
	if *backupDir != "" && *restoreFrom != "" {
		return errors.New("backup and restore are mutually exclusive")
	}
	if *restoreFrom != "" {
		return backup.Restore(*restoreFrom, cfg.Global.DataDir)
	}
	if *backupDir != "" {
		return backup.Create(cfg.Global.DataDir, *backupDir)
	}
	if err := os.MkdirAll(cfg.Global.DataDir, 0o755); err != nil {
		return err
	}

	lock, err := backup.Lock(cfg.Global.DataDir)
	if err != nil {
		return err
	}
	defer lock.Close()
	passwordFile, err := cfg.EnsureWebAuth()
	if err != nil {
		return fmt.Errorf("web authentication: %w", err)
	}
	if passwordFile != "" {
		log.Warn("dashboard login required; read the password file on this server", "password_file", passwordFile)
	}

	h, err := hostIdentity(cfg)
	if err != nil {
		return err
	}
	log.Info("monitord starting", "version", version, "host", h.Hostname, "os", h.OS, "arch", h.Arch, "update_every", h.UpdateEvery)

	retSize, err := config.ParseSize(cfg.DB.Tier0RetentionSize)
	if err != nil {
		return err
	}
	tiers := tsdb.DefaultTiers()
	if cfg.DB.Tiers < 1 || cfg.DB.Tiers > len(tiers)+1 {
		return fmt.Errorf("db.tiers must be between 1 and %d", len(tiers)+1)
	}
	tiers = tiers[:cfg.DB.Tiers-1]
	for i, ret := range []time.Duration{cfg.DB.Tier1Retention, cfg.DB.Tier2Retention} {
		if i < len(tiers) {
			tiers[i].Retention = ret
		}
	}
	db, err := tsdb.Open(tsdb.Options{
		Dir:           filepath.Join(cfg.Global.DataDir, "db"),
		Retention:     cfg.DB.Tier0Retention,
		RetentionSize: retSize,
		Checkpoint:    cfg.DB.Checkpoint,
		Tiers:         tiers,
		Logger:        log.With("component", "tsdb"),
	})
	if err != nil {
		return fmt.Errorf("open tsdb: %w", err)
	}

	reg := registry.New(h, db)
	disabled := map[string]bool{}
	for _, n := range cfg.Collectors.Disabled {
		disabled[n] = true
	}
	sched := collect.NewScheduler(reg, log.With("component", "collect"), collect.Options{Names: cfg.Collectors.Enabled, Disabled: disabled, Modules: cfg.ModuleDecoders()})

	var eng *health.Engine
	if cfg.HealthEnabled() {
		eng, err = newHealth(cfg, *cfgPath, reg, db, log.With("component", "health"))
		if err != nil {
			return fmt.Errorf("health: %w", err)
		}
		if ml := sched.Collector("ml"); ml != nil {
			if src, ok := ml.(health.AnomalySource); ok {
				eng.SetAnomaly(src)
			}
		}
	}

	var pm *plugins.Manager
	if cfg.PluginsEnabled() {
		pdir := cfg.Plugins.Dir
		if pdir != "" && !filepath.IsAbs(pdir) {
			pdir = filepath.Join(filepath.Dir(*cfgPath), pdir)
		}
		pm = plugins.New(reg, cfg.Plugins.List, plugins.Options{
			Dir:      pdir,
			Disabled: cfg.Plugins.Disabled,
			Logger:   log.With("component", "plugins"),
		})
	}

	switch cfg.Mode {
	case "", "agent", "hub":
	default:
		return fmt.Errorf("mode must be agent or hub, got %q", cfg.Mode)
	}
	var users []api.User
	for _, u := range cfg.Web.Users {
		users = append(users, api.User{Name: u.Name, Token: u.Token, Role: api.Role(u.Role)})
	}

	var sc *stream.Client
	if cfg.Stream.Enabled {
		if len(cfg.Stream.Destinations) == 0 {
			return errors.New("stream.enabled requires stream.destinations")
		}
		var alarms func() []health.Alarm
		if eng != nil {
			alarms = eng.Alarms
		}
		sc = stream.NewClient(reg, db, stream.ClientOptions{
			Alarms:             alarms,
			Destinations:       cfg.Stream.Destinations,
			APIKey:             cfg.Stream.APIKey,
			ClaimToken:         cfg.Stream.ClaimToken,
			InsecureSkipVerify: cfg.Stream.InsecureSkipVerify,
			Timeout:            cfg.Stream.Timeout,
			Replicate:          cfg.Stream.Replicate,
			Protocol:           cfg.Stream.Protocol,
			Version:            version,
			Functions:          sched.Functions,
			Logger:             log.With("component", "stream"),
			OnConfig: func(disabled []string) {
				for _, n := range disabled {
					sched.SetEnabled(n, false)
				}
			},
		})
	}

	apiOpt := api.Options{
		OperationsDir: filepath.Join(cfg.Global.DataDir, "operations"),
		Version:       version,
		Mode:          cfg.Mode,
		StartedAt:     time.Now(),
		AllowFrom:     cfg.Web.AllowFrom,
		Token:         cfg.Web.Token,
		Users:         users,
		Health:        eng,
		Plugins:       pm,
		Stream:        sc,
		Logger:        log.With("component", "api"),
	}
	var nodes *hub.Nodes
	var cluster *hub.Cluster
	var org *hub.Org
	var srv *api.Server
	if cfg.Mode == "hub" {
		hubDir := filepath.Join(cfg.Global.DataDir, "hub")
		org, err = hub.OpenOrg(hubDir)
		if err != nil {
			return fmt.Errorf("hub org: %w", err)
		}
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
			Keys:             cfg.Hub.APIKeys,
			ExtraKeys:        org.Keys,
			Replicate:        cfg.Hub.Replicate,
			MaxNodes:         cfg.Hub.MaxNodes,
			MaxChartsPerNode: cfg.Hub.MaxChartsPerNode,
			MaxDimsPerChart:  cfg.Hub.MaxDimsPerChart,
			Storage:          cfg.Hub.Storage,
			NodeConfig: func(nodeID string) []string {
				cfg, ok := org.GetConfig(nodeID)
				if !ok {
					return nil
				}
				return cfg.Disabled
			},
			Logger:   log.With("component", "hub"),
			OnSample: func(n, c string, t int64, v map[string]float64) { srv.PublishNodeSample(n, c, t, v) },
			OnAlarm:  func(n string, e health.LogEntry) { srv.PublishNodeAlarm(n, e) },
		})
		if err != nil {
			return fmt.Errorf("hub: %w", err)
		}
		apiOpt.Nodes = nodes
		apiOpt.Org = org
		apiOpt.PeerToken = cfg.Hub.PeerToken
		apiOpt.ExtraFunctions = []collect.Function{streamingFunction(nodes)}
		if len(cfg.Hub.Peers) > 0 {
			cluster = hub.NewCluster(cfg.Hub.Peers, cfg.Hub.PeerToken, log.With("component", "cluster"))
			cluster.SetNodes(nodes)
			apiOpt.Cluster = cluster
		}
	}
	if cfg.Web.OIDC.Issuer != "" || cfg.Web.OIDC.ClientID != "" {
		apiOpt.OIDC = &api.OIDCConfig{Issuer: cfg.Web.OIDC.Issuer, ClientID: cfg.Web.OIDC.ClientID,
			ClientSecret: cfg.Web.OIDC.ClientSecret, RedirectURL: cfg.Web.OIDC.RedirectURL, Role: cfg.Web.OIDC.Role}
	}
	if cfg.Web.LDAP.URL != "" {
		apiOpt.LDAP = &api.LDAPConfig{URL: cfg.Web.LDAP.URL, UserDN: cfg.Web.LDAP.UserDN,
			BindDN: cfg.Web.LDAP.BindDN, BindPass: cfg.Web.LDAP.BindPass, Role: cfg.Web.LDAP.Role}
	}
	srv, err = api.New(reg, db, sched, apiOpt)
	if err != nil {
		return err
	}
	if eng != nil {
		eng.SetOnEvent(func(e health.LogEntry) {
			srv.PublishAlarm(e)
			if sc != nil {
				sc.PublishAlarm(e)
			}
		})
	}
	httpSrv := &http.Server{
		Addr:              cfg.Web.Listen,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	schedDone := make(chan struct{})
	go func() {
		defer close(schedDone)
		sched.Run(ctx)
	}()
	healthDone := make(chan struct{})
	go func() {
		defer close(healthDone)
		if eng != nil {
			eng.Run(ctx)
		}
	}()
	pluginsDone := make(chan struct{})
	go func() {
		defer close(pluginsDone)
		if pm != nil {
			pm.Run(ctx)
		}
	}()
	streamDone := make(chan struct{})
	go func() {
		defer close(streamDone)
		var wg sync.WaitGroup
		if sc != nil {
			wg.Add(1)
			go func() { defer wg.Done(); sc.Run(ctx) }()
		}
		if nodes != nil {
			wg.Add(1)
			go func() { defer wg.Done(); nodes.Run(ctx) }()
		}
		if cluster != nil {
			wg.Add(1)
			go func() { defer wg.Done(); cluster.Run(ctx) }()
		}
		if exp := export.New(reg, cfg.Export.Destinations, log.With("component", "export")); !exp.Empty() {
			wg.Add(1)
			go func() { defer wg.Done(); exp.Run(ctx) }()
		}
		wg.Wait()
	}()

	if port := discover.Port(cfg.Web.Listen); port > 0 {
		go discover.Announce(ctx, reg.Host.Hostname, port)
	}

	errc := make(chan error, 1)
	go func() {
		// An explicit -listen flag overrides web.enabled=false; otherwise a
		// headless agent can drop the HTTP listener entirely.
		if !cfg.WebEnabled() && *listen == "" {
			log.Info("web server disabled (web.enabled=false)")
			return
		}
		log.Info("web server listening", "addr", cfg.Web.Listen)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- err
		}
	}()

	var runErr error
	select {
	case <-ctx.Done():
		log.Info("shutting down")
	case runErr = <-errc:
		stop()
		log.Error("web server failed", "err", runErr)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(shutdownCtx)
	select {
	case <-schedDone:
	case <-shutdownCtx.Done():
		log.Warn("collectors did not stop in time")
	}
	select {
	case <-healthDone:
	case <-shutdownCtx.Done():
		log.Warn("health engine did not stop in time")
	}
	select {
	case <-pluginsDone:
	case <-shutdownCtx.Done():
		log.Warn("plugins did not stop in time")
	}
	select {
	case <-streamDone:
	case <-shutdownCtx.Done():
		log.Warn("stream/hub did not stop in time")
	}
	if err := db.Close(); err != nil {
		log.Error("tsdb close", "err", err)
	}
	return runErr
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

func hostIdentity(cfg *config.Config) (*registry.Host, error) {
	info, err := host.Info()
	if err != nil {
		return nil, fmt.Errorf("host info: %w", err)
	}
	name := cfg.Global.Hostname
	if name == "" {
		name = info.Hostname
	}
	if name == "" {
		name, _ = os.Hostname()
	}
	id := info.HostID
	if id == "" {
		id = name
	}
	return &registry.Host{
		ID:          id,
		Hostname:    name,
		OS:          runtime.GOOS,
		Arch:        runtime.GOARCH,
		UpdateEvery: cfg.Global.UpdateEvery,
		Labels: map[string]string{
			"_os_name":        info.Platform,
			"_os_version":     info.PlatformVersion,
			"_kernel":         info.KernelVersion,
			"_virtualization": strings.TrimSpace(info.VirtualizationSystem + " " + info.VirtualizationRole),
			"_agent_version":  version,
		},
	}, nil
}

func newHealth(cfg *config.Config, cfgPath string, reg *registry.Registry, db *tsdb.Store, log *slog.Logger) (*health.Engine, error) {
	var sets [][]*health.Rule
	if cfg.HealthBuiltin() {
		builtin, err := health.DefaultRules()
		if err != nil {
			return nil, err
		}
		sets = append(sets, builtin)
	}
	dir := cfg.Health.Dir
	if dir != "" && !filepath.IsAbs(dir) {
		dir = filepath.Join(filepath.Dir(cfgPath), dir)
	}
	if dir != "" {
		custom, err := health.LoadDir(dir)
		if err != nil {
			return nil, err
		}
		sets = append(sets, custom)
	}
	inline, err := health.CompileAll(cfg.Health.Alarms, cfgPath)
	if err != nil {
		return nil, err
	}
	sets = append(sets, inline)
	rules := health.Merge(sets...)

	var notifiers []health.Notifier
	n := cfg.Health.Notify
	if n.Webhook.URL != "" {
		notifiers = append(notifiers, &health.WebhookNotifier{URL: n.Webhook.URL, Headers: n.Webhook.Headers})
	}
	if n.Slack.WebhookURL != "" {
		notifiers = append(notifiers, &health.SlackNotifier{WebhookURL: n.Slack.WebhookURL, Channel: n.Slack.Channel})
	}
	if n.Email.Server != "" && len(n.Email.To) > 0 {
		notifiers = append(notifiers, &health.EmailNotifier{Server: n.Email.Server, From: n.Email.From, To: n.Email.To,
			Username: n.Email.Username, Password: n.Email.Password, Insecure: n.Email.Insecure})
	}
	if n.DingTalk.WebhookURL != "" {
		notifiers = append(notifiers, &health.ChatNotifier{Kind: "dingtalk", WebhookURL: n.DingTalk.WebhookURL})
	}
	if n.WeCom.WebhookURL != "" {
		notifiers = append(notifiers, &health.ChatNotifier{Kind: "wecom", WebhookURL: n.WeCom.WebhookURL})
	}
	if n.Feishu.WebhookURL != "" {
		notifiers = append(notifiers, &health.ChatNotifier{Kind: "feishu", WebhookURL: n.Feishu.WebhookURL})
	}
	if n.Telegram.Token != "" && n.Telegram.ChatID != "" {
		notifiers = append(notifiers, &health.TelegramNotifier{Token: n.Telegram.Token, ChatID: n.Telegram.ChatID})
	}
	if n.Discord.WebhookURL != "" {
		notifiers = append(notifiers, &health.DiscordNotifier{WebhookURL: n.Discord.WebhookURL})
	}
	if n.PagerDuty.RoutingKey != "" {
		notifiers = append(notifiers, &health.PagerDutyNotifier{RoutingKey: n.PagerDuty.RoutingKey})
	}
	if n.Push.URL != "" {
		notifiers = append(notifiers, &health.PushNotifier{URL: n.Push.URL, Headers: n.Push.Headers})
	}
	if n.APNs.Key != "" && n.APNs.DeviceToken != "" {
		notifiers = append(notifiers, &health.APNsNotifier{KeyPEM: n.APNs.Key, KeyID: n.APNs.KeyID, TeamID: n.APNs.TeamID, Topic: n.APNs.Topic, DeviceToken: n.APNs.DeviceToken})
	}
	if n.FCM.ServerKey != "" && n.FCM.Token != "" {
		notifiers = append(notifiers, &health.FCMNotifier{ServerKey: n.FCM.ServerKey, Token: n.FCM.Token})
	}
	if n.Huawei.AppID != "" && n.Huawei.Token != "" && n.Huawei.RegID != "" {
		notifiers = append(notifiers, &health.HuaweiNotifier{AppID: n.Huawei.AppID, Token: n.Huawei.Token, RegID: n.Huawei.RegID})
	}
	if n.Xiaomi.AppSecret != "" && n.Xiaomi.RegID != "" {
		notifiers = append(notifiers, &health.XiaomiNotifier{AppSecret: n.Xiaomi.AppSecret, Package: n.Xiaomi.Package, RegID: n.Xiaomi.RegID})
	}
	if n.SMS.Phone != "" && (n.SMS.URL != "" || n.SMS.AccessKey != "") {
		notifiers = append(notifiers, &health.SMSNotifier{Provider: n.SMS.Provider, AccessKey: n.SMS.AccessKey, Secret: n.SMS.Secret, SignName: n.SMS.SignName, Template: n.SMS.Template, Phone: n.SMS.Phone, URL: n.SMS.URL})
	}

	vars := map[string]float64{"cpus": float64(runtime.NumCPU())}
	if vm, err := mem.VirtualMemory(); err == nil {
		vars["ram_total"] = float64(vm.Total) / (1024 * 1024)
	}
	log.Info("health engine", "rules", len(rules), "notifiers", len(notifiers), "silent", cfg.Health.Silent)
	return health.New(reg, db, health.Options{
		Rules:      rules,
		Hostname:   reg.Host.Hostname,
		LogDir:     filepath.Join(cfg.Global.DataDir, "health"),
		LogKeep:    cfg.Health.LogKeep,
		Notifiers:  notifiers,
		Roles:      n.Roles,
		HostVars:   vars,
		Logger:     log,
		SilenceAll: cfg.Health.Silent,
		Windows:    cfg.Health.Windows,
	})
}
