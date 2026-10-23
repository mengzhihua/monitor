package main

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/mem"

	"github.com/mengzhihua/monitor/core/internal/config"
	"github.com/mengzhihua/monitor/core/internal/health"
	"github.com/mengzhihua/monitor/core/internal/registry"
	"github.com/mengzhihua/monitor/core/internal/tsdb"
)

func newHealth(cfg *config.Config, cfgPath string, reg *registry.Registry, db *tsdb.Store, log *slog.Logger, runner *autoCommandRunner) (*health.Engine, error) {
	var sets [][]*health.Rule
	if cfg.HealthBuiltin() {
		builtin, err := health.DefaultRulesWith(cfg.Health.Macros)
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
		custom, err := health.LoadDirWith(dir, cfg.Health.Macros)
		if err != nil {
			return nil, err
		}
		sets = append(sets, custom)
	}
	inline, err := health.CompileAllWith(cfg.Health.Alarms, cfgPath, cfg.Health.Macros)
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
	if n.Email.Server != "" || n.Email.From != "" || len(n.Email.To) > 0 || n.Email.Username != "" || n.Email.Password != "" {
		email := &health.EmailNotifier{Server: n.Email.Server, From: n.Email.From, To: n.Email.To,
			Username: n.Email.Username, Password: n.Email.Password, Insecure: n.Email.Insecure, TLSMode: n.Email.TLSMode}
		if err := email.Validate(); err != nil {
			return nil, err
		}
		notifiers = append(notifiers, email)
	}
	if n.DingTalk.WebhookURL != "" {
		notifiers = append(notifiers, &health.ChatNotifier{Kind: "dingtalk", WebhookURL: n.DingTalk.WebhookURL})
	}
	if n.WeCom.WebhookURL != "" {
		notifiers = append(notifiers, &health.ChatNotifier{Kind: "wecom", WebhookURL: n.WeCom.WebhookURL})
	}
	if n.Feishu.WebhookURL != "" {
		notifiers = append(notifiers, &health.ChatNotifier{Kind: "feishu", WebhookURL: n.Feishu.WebhookURL, Secret: n.Feishu.Secret})
	}
	if n.Ntfy.URL != "" || n.Ntfy.Topic != "" || n.Ntfy.Token != "" {
		notifier := &health.NtfyNotifier{URL: n.Ntfy.URL, Topic: n.Ntfy.Topic, Token: n.Ntfy.Token}
		if err := notifier.Validate(); err != nil {
			return nil, fmt.Errorf("ntfy: %w", err)
		}
		notifiers = append(notifiers, notifier)
	}
	if n.Gotify.URL != "" || n.Gotify.Token != "" {
		notifier := &health.GotifyNotifier{URL: n.Gotify.URL, Token: n.Gotify.Token}
		if err := notifier.Validate(); err != nil {
			return nil, fmt.Errorf("gotify: %w", err)
		}
		notifiers = append(notifiers, notifier)
	}
	if n.Bark.URL != "" || n.Bark.DeviceKey != "" {
		notifier := &health.BarkNotifier{URL: n.Bark.URL, DeviceKey: n.Bark.DeviceKey, Group: n.Bark.Group}
		if err := notifier.Validate(); err != nil {
			return nil, fmt.Errorf("bark: %w", err)
		}
		notifiers = append(notifiers, notifier)
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
	if n.Script.Path != "" {
		notifiers = append(notifiers, &health.ScriptNotifier{Path: n.Script.Path, Args: n.Script.Args})
	}

	vars := map[string]float64{"cpus": float64(runtime.NumCPU())}
	if vm, err := mem.VirtualMemory(); err == nil {
		vars["ram_total"] = float64(vm.Total) / (1024 * 1024)
	}
	log.Info("health engine", "rules", len(rules), "notifiers", len(notifiers), "silent", cfg.Health.Silent)
	inhibit := true
	if cfg.Health.InhibitSameChart != nil {
		inhibit = *cfg.Health.InhibitSameChart
	}
	groupWait, err := optionalDuration(cfg.Health.GroupWait)
	if err != nil {
		return nil, fmt.Errorf("health.group_wait: %w", err)
	}
	escalateAfter, err := optionalDuration(cfg.Health.EscalateAfter)
	if err != nil {
		return nil, fmt.Errorf("health.escalate_after: %w", err)
	}
	return health.New(reg, db, health.Options{
		Rules:             rules,
		Hostname:          reg.Host.Hostname,
		LogDir:            filepath.Join(cfg.Global.DataDir, "health"),
		LogKeep:           cfg.Health.LogKeep,
		Notifiers:         notifiers,
		Roles:             n.Roles,
		HostVars:          vars,
		Logger:            log,
		SilenceAll:        cfg.Health.Silent,
		InhibitSameChart:  inhibit,
		Inhibit:           cfg.Health.Inhibit,
		GroupWait:         groupWait,
		EscalateAfter:     escalateAfter,
		EscalateTo:        cfg.Health.EscalateTo,
		OnCall:            cfg.Health.OnCall,
		Windows:           cfg.Health.Windows,
		Macros:            cfg.Health.Macros,
		Actions:           cfg.Health.Actions,
		Correlation:       cfg.Health.Correlation,
		RunCommand:        runner.Run,
		OnActionInhibited: runner.NoteInhibited,
	})
}

func optionalDuration(raw string) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	return time.ParseDuration(raw)
}
