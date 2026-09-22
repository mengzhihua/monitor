package collect

// Stock Prometheus → native-ID profiles. Only exporters without a dedicated
// go.d collector; unique prefixes may auto-select. Generic families (http_*,
// go_*) require an explicit job.profile.

var stockPromProfiles = []PromProfile{
	{
		Name: "etcd", Title: "etcd", Auto: true,
		Match: []string{"etcd_"},
		Notes: "etcd /metrics (client URL :2379/metrics)",
		Charts: []PromChart{
			{ID: "etcd.has_leader", Title: "etcd Has Leader", Units: "boolean", Metric: "etcd_server_has_leader"},
			{ID: "etcd.leader_changes", Title: "etcd Leader Changes", Units: "changes/s", Metric: "etcd_server_leader_changes_seen_total", Counter: true},
			{ID: "etcd.proposals_committed", Title: "etcd Proposals Committed", Units: "proposals/s", Metric: "etcd_server_proposals_committed_total", Counter: true},
			{ID: "etcd.proposals_applied", Title: "etcd Proposals Applied", Units: "proposals/s", Metric: "etcd_server_proposals_applied_total", Counter: true},
			{ID: "etcd.proposals_pending", Title: "etcd Proposals Pending", Units: "proposals", Metric: "etcd_server_proposals_pending"},
			{ID: "etcd.proposals_failed", Title: "etcd Proposals Failed", Units: "proposals/s", Metric: "etcd_server_proposals_failed_total", Counter: true},
			{ID: "etcd.db_size", Title: "etcd MVCC DB Size", Units: "bytes", Metric: "etcd_mvcc_db_total_size_in_bytes", Type: "area"},
			{ID: "etcd.client_traffic", Title: "etcd Client gRPC Traffic", Units: "bytes/s", Type: "area",
				Metric: "etcd_network_client_grpc_received_bytes_total", DimName: "received", Counter: true},
		},
	},
	{
		Name: "minio", Title: "MinIO", Auto: true,
		Match: []string{"minio_"},
		Notes: "MinIO cluster /minio/v2/metrics/cluster",
		Charts: []PromChart{
			{ID: "minio.cluster_health", Title: "MinIO Cluster Health", Units: "status", Metric: "minio_cluster_health_status"},
			{ID: "minio.nodes_online", Title: "MinIO Nodes Online", Units: "nodes", Metric: "minio_cluster_nodes_online_total"},
			{ID: "minio.capacity_usable", Title: "MinIO Usable Capacity", Units: "bytes", Metric: "minio_cluster_capacity_usable_total_bytes", Type: "area"},
			{ID: "minio.capacity_free", Title: "MinIO Usable Free", Units: "bytes", Metric: "minio_cluster_capacity_usable_free_bytes", Type: "area"},
			{ID: "minio.s3_requests", Title: "MinIO S3 Requests", Units: "requests/s", Metric: "minio_s3_requests_total", DimLabel: "api", Counter: true, Type: "stacked"},
			{ID: "minio.s3_errors", Title: "MinIO S3 Errors", Units: "errors/s", Metric: "minio_s3_requests_errors_total", DimLabel: "api", Counter: true},
		},
	},
	{
		Name: "vault", Title: "Vault", Auto: true,
		Match: []string{"vault_"},
		Notes: "HashiCorp Vault /metrics (unauthenticated prometheus enabled)",
		Charts: []PromChart{
			{ID: "vault.unsealed", Title: "Vault Unsealed", Units: "boolean", Metric: "vault_core_unsealed"},
			{ID: "vault.active", Title: "Vault Active", Units: "boolean", Metric: "vault_core_active"},
			{ID: "vault.leases", Title: "Vault Token Leases", Units: "leases", Metric: "vault_expire_num_leases"},
			{ID: "vault.audit_failures", Title: "Vault Audit Log Failures", Units: "failures/s", Metric: "vault_audit_log_request_failure", Counter: true},
			{ID: "vault.runtime_alloc", Title: "Vault Allocated Memory", Units: "bytes", Metric: "vault_runtime_alloc_bytes", Type: "area"},
		},
	},
	{
		Name: "jenkins", Title: "Jenkins", Auto: true,
		Match: []string{"jenkins_", "default_jenkins_"},
		Notes: "Jenkins Prometheus plugin",
		Charts: []PromChart{
			{ID: "jenkins.executors_available", Title: "Jenkins Executors Available", Units: "executors", Metric: "default_jenkins_executors_available"},
			{ID: "jenkins.executors_busy", Title: "Jenkins Executors Busy", Units: "executors", Metric: "default_jenkins_executors_busy"},
			{ID: "jenkins.queue", Title: "Jenkins Queue Size", Units: "jobs", Metric: "default_jenkins_queue_size_value"},
			{ID: "jenkins.plugins_active", Title: "Jenkins Active Plugins", Units: "plugins", Metric: "jenkins_plugins_active"},
			{ID: "jenkins.plugins_failed", Title: "Jenkins Failed Plugins", Units: "plugins", Metric: "jenkins_plugins_failed"},
			{ID: "jenkins.health_score", Title: "Jenkins Health Score", Units: "%", Metric: "jenkins_health_check_score"},
		},
	},
	{
		Name: "grafana", Title: "Grafana", Auto: true,
		Match: []string{"grafana_"},
		Notes: "Grafana /metrics",
		Charts: []PromChart{
			{ID: "grafana.dashboards", Title: "Grafana Dashboards", Units: "dashboards", Metric: "grafana_stat_totals_dashboard"},
			{ID: "grafana.users", Title: "Grafana Users", Units: "users", Metric: "grafana_stat_totals_users"},
			{ID: "grafana.alerting_alerts", Title: "Grafana Alerting Active Alerts", Units: "alerts", Metric: "grafana_alerting_alertmanagers_discovered"},
			{ID: "grafana.api_status", Title: "Grafana API Responses", Units: "responses/s", Metric: "grafana_http_request_duration_seconds_count", DimLabel: "status_code", Counter: true, Type: "stacked"},
		},
	},
	{
		Name: "prometheus", Title: "Prometheus server", Auto: true,
		Match: []string{"prometheus_tsdb_", "prometheus_rule_", "prometheus_notifications_", "prometheus_sd_", "prometheus_config_"},
		Notes: "Prometheus server /metrics (not client library)",
		Charts: []PromChart{
			{ID: "prometheus.tsdb_series", Title: "Prometheus Head Series", Units: "series", Metric: "prometheus_tsdb_head_series"},
			{ID: "prometheus.tsdb_blocks", Title: "Prometheus TSDB Blocks Size", Units: "bytes", Metric: "prometheus_tsdb_storage_blocks_bytes", Type: "area"},
			{ID: "prometheus.notifications_dropped", Title: "Prometheus Notifications Dropped", Units: "alerts/s", Metric: "prometheus_notifications_dropped_total", Counter: true},
			{ID: "prometheus.rule_missed", Title: "Prometheus Rule Iterations Missed", Units: "iterations/s", Metric: "prometheus_rule_group_iterations_missed_total", Counter: true},
			{ID: "prometheus.targets", Title: "Prometheus Discovered Targets", Units: "targets", Metric: "prometheus_sd_discovered_targets", DimLabel: "name"},
			{ID: "prometheus.config_reload_ok", Title: "Prometheus Config Reload Successful", Units: "boolean", Metric: "prometheus_config_last_reload_successful"},
		},
	},
	{
		Name: "alertmanager", Title: "Alertmanager", Auto: true,
		Match: []string{"alertmanager_"},
		Notes: "Prometheus Alertmanager /metrics",
		Charts: []PromChart{
			{ID: "alertmanager.alerts", Title: "Alertmanager Alerts", Units: "alerts", Metric: "alertmanager_alerts", DimLabel: "state", Type: "stacked"},
			{ID: "alertmanager.notifications", Title: "Alertmanager Notifications", Units: "notifications/s", Metric: "alertmanager_notifications_total", DimLabel: "integration", Counter: true},
			{ID: "alertmanager.notifications_failed", Title: "Alertmanager Notifications Failed", Units: "notifications/s", Metric: "alertmanager_notifications_failed_total", DimLabel: "integration", Counter: true},
			{ID: "alertmanager.cluster_members", Title: "Alertmanager Cluster Members", Units: "members", Metric: "alertmanager_cluster_members"},
		},
	},
	{
		Name: "kafka", Title: "Kafka exporter", Auto: true,
		Match: []string{"kafka_brokers", "kafka_topic_", "kafka_consumergroup_"},
		Notes: "danielqsj/kafka_exporter (not Kafka REST export)",
		Charts: []PromChart{
			{ID: "kafka.brokers", Title: "Kafka Brokers", Units: "brokers", Metric: "kafka_brokers"},
			{ID: "kafka.topic_partitions", Title: "Kafka Topic Partitions", Units: "partitions", Metric: "kafka_topic_partitions", DimLabel: "topic"},
			{ID: "kafka.under_replicated", Title: "Kafka Under-Replicated Partitions", Units: "partitions", Metric: "kafka_topic_partition_under_replicated_partition", DimLabel: "topic"},
			{ID: "kafka.consumergroup_lag", Title: "Kafka Consumer Group Lag", Units: "messages", Metric: "kafka_consumergroup_lag", DimLabel: "consumergroup"},
		},
	},
	{
		Name: "blackbox", Title: "Blackbox exporter", Auto: true,
		Match: []string{"probe_success", "probe_duration_seconds", "probe_http_", "probe_ssl_"},
		Notes: "prometheus/blackbox_exporter",
		Charts: []PromChart{
			{ID: "blackbox.probe_success", Title: "Blackbox Probe Success", Units: "boolean", Metric: "probe_success"},
			{ID: "blackbox.probe_duration", Title: "Blackbox Probe Duration", Units: "seconds", Metric: "probe_duration_seconds"},
			{ID: "blackbox.http_status", Title: "Blackbox HTTP Status", Units: "code", Metric: "probe_http_status_code"},
			{ID: "blackbox.ssl_expiry", Title: "Blackbox SSL Certificate Expiry", Units: "seconds", Metric: "probe_ssl_earliest_cert_expiry"},
		},
	},
	{
		Name: "gitlab", Title: "GitLab", Auto: true,
		Match: []string{"gitlab_", "sidekiq_jobs_"},
		Notes: "GitLab Omnibus / Prometheus metrics",
		Charts: []PromChart{
			{ID: "gitlab.http_requests", Title: "GitLab HTTP Requests", Units: "requests/s", Metric: "http_requests_total", DimLabel: "status", Counter: true, Type: "stacked"},
			{ID: "gitlab.sidekiq_failed", Title: "GitLab Sidekiq Failed Jobs", Units: "jobs/s", Metric: "sidekiq_jobs_failed_total", Counter: true},
			{ID: "gitlab.db_pool_busy", Title: "GitLab DB Pool Busy", Units: "connections", Metric: "gitlab_database_connection_pool_busy"},
			{ID: "gitlab.workhorse_requests", Title: "GitLab Workhorse Requests", Units: "requests/s", Metric: "gitlab_workhorse_http_requests_total", Counter: true},
		},
	},
	{
		Name: "harbor", Title: "Harbor", Auto: true,
		Match: []string{"harbor_", "projectquota_"},
		Notes: "Harbor /metrics",
		Charts: []PromChart{
			{ID: "harbor.health", Title: "Harbor Health", Units: "boolean", Metric: "harbor_health"},
			{ID: "harbor.projects", Title: "Harbor Projects", Units: "projects", Metric: "harbor_project_total", DimLabel: "type"},
			{ID: "harbor.quota_usage", Title: "Harbor Project Quota Usage", Units: "bytes", Metric: "projectquota_usage_byte", DimLabel: "id"},
		},
	},
	{
		Name: "argocd", Title: "Argo CD", Auto: true,
		Match: []string{"argocd_"},
		Notes: "Argo CD metrics servers",
		Charts: []PromChart{
			{ID: "argocd.apps", Title: "Argo CD Applications", Units: "apps", Metric: "argocd_app_info", DimLabel: "dest_namespace"},
			{ID: "argocd.syncs", Title: "Argo CD Application Syncs", Units: "syncs/s", Metric: "argocd_app_sync_total", DimLabel: "dest_server", Counter: true},
			{ID: "argocd.cluster_events", Title: "Argo CD Cluster API Events", Units: "events/s", Metric: "argocd_cluster_api_resource_events_total", Counter: true},
		},
	},
	{
		Name: "cert_manager", Title: "cert-manager", Auto: true,
		Match: []string{"certmanager_", "cert_manager_"},
		Notes: "cert-manager controller /metrics",
		Charts: []PromChart{
			{ID: "cert_manager.ready", Title: "cert-manager Certificate Ready", Units: "certs", Metric: "certmanager_certificate_ready_status", DimLabel: "condition", Type: "stacked"},
			{ID: "cert_manager.expiry", Title: "cert-manager Certificate Expiry", Units: "seconds", Metric: "certmanager_certificate_expiration_timestamp_seconds", DimLabel: "name"},
			{ID: "cert_manager.acme_requests", Title: "cert-manager ACME Client Requests", Units: "requests/s", Metric: "certmanager_http_acme_client_request_count", DimLabel: "status", Counter: true},
		},
	},
	{
		Name: "cilium", Title: "Cilium", Auto: true,
		Match: []string{"cilium_"},
		Notes: "Cilium agent Prometheus metrics",
		Charts: []PromChart{
			{ID: "cilium.endpoints", Title: "Cilium Endpoint State", Units: "endpoints", Metric: "cilium_endpoint_state", DimLabel: "endpoint_state", Type: "stacked"},
			{ID: "cilium.drops", Title: "Cilium Dropped Packets", Units: "packets/s", Metric: "cilium_drop_count_total", DimLabel: "reason", Counter: true},
			{ID: "cilium.forward", Title: "Cilium Forwarded Packets", Units: "packets/s", Metric: "cilium_forward_count_total", DimLabel: "direction", Counter: true},
			{ID: "cilium.unreachable_nodes", Title: "Cilium Unreachable Nodes", Units: "nodes", Metric: "cilium_unreachable_nodes"},
		},
	},
	{
		Name: "istio", Title: "Istio", Auto: true,
		Match: []string{"istio_", "pilot_xds_"},
		Notes: "Istio telemetry / Pilot (not the dedicated envoy collector)",
		Charts: []PromChart{
			{ID: "istio.requests", Title: "Istio Requests", Units: "requests/s", Metric: "istio_requests_total", DimLabel: "response_code", Counter: true, Type: "stacked"},
			{ID: "istio.request_duration_count", Title: "Istio Request Duration Count", Units: "requests/s", Metric: "istio_request_duration_milliseconds_count", Counter: true},
			{ID: "istio.xds_pushes", Title: "Istio Pilot XDS Pushes", Units: "pushes/s", Metric: "pilot_xds_pushes", DimLabel: "type", Counter: true},
		},
	},
	{
		Name: "vllm", Title: "vLLM", Auto: true,
		Match: []string{"vllm_", "vllm:"},
		Notes: "vLLM /metrics",
		Charts: []PromChart{
			{ID: "vllm.requests_running", Title: "vLLM Requests Running", Units: "requests", Metric: "vllm:num_requests_running"},
			{ID: "vllm.requests_waiting", Title: "vLLM Requests Waiting", Units: "requests", Metric: "vllm:num_requests_waiting"},
			{ID: "vllm.gpu_cache", Title: "vLLM GPU Cache Usage", Units: "%", Metric: "vllm:gpu_cache_usage_perc"},
			{ID: "vllm.requests_running_alt", Title: "vLLM Requests Running", Units: "requests", Metric: "vllm_num_requests_running"},
			{ID: "vllm.requests_waiting_alt", Title: "vLLM Requests Waiting", Units: "requests", Metric: "vllm_num_requests_waiting"},
		},
	},
	{
		Name: "litellm", Title: "LiteLLM", Auto: true,
		Match: []string{"litellm_"},
		Notes: "LiteLLM proxy /metrics",
		Charts: []PromChart{
			{ID: "litellm.requests", Title: "LiteLLM Total Requests", Units: "requests/s", Metric: "litellm_request_total", DimLabel: "model", Counter: true},
			{ID: "litellm.failed", Title: "LiteLLM Failed Requests", Units: "requests/s", Metric: "litellm_llm_api_failed_requests_metric", DimLabel: "model", Counter: true},
			{ID: "litellm.spend", Title: "LiteLLM Spend", Units: "currency", Metric: "litellm_total_spend_metric", DimLabel: "model"},
			{ID: "litellm.deployment_state", Title: "LiteLLM Deployment State", Units: "state", Metric: "litellm_remaining_requests_metric", DimLabel: "api_base"},
		},
	},
	{
		Name: "fastapi", Title: "FastAPI", Auto: false,
		Match: []string{"http_requests_total", "http_request_duration_seconds", "http_requests_inprogress"},
		Notes: "prometheus-fastapi-instrumentator; set profile: fastapi (http_* is too generic for auto)",
		Charts: []PromChart{
			{ID: "fastapi.requests", Title: "FastAPI Request Outcomes", Units: "requests/s", Metric: "http_requests_total", DimLabel: "status", Counter: true, Type: "stacked"},
			{ID: "fastapi.inprogress", Title: "FastAPI Requests In Progress", Units: "requests", Metric: "http_requests_inprogress", DimLabel: "handler"},
			{ID: "fastapi.request_count", Title: "FastAPI Request Measurements", Units: "requests/s", Metric: "http_request_duration_seconds_count", Counter: true},
			{ID: "fastapi.request_time", Title: "FastAPI Completed Request Time", Units: "seconds/s", Metric: "http_request_duration_seconds_sum", Counter: true},
		},
	},
	{
		Name: "go_runtime", Title: "Go runtime", Auto: false,
		Match: []string{"go_goroutines", "go_memstats_", "go_gc_"},
		Notes: "Go client library; set profile: go_runtime (almost every Go exporter emits these)",
		Charts: []PromChart{
			{ID: "go.goroutines", Title: "Go Goroutines", Units: "goroutines", Metric: "go_goroutines"},
			{ID: "go.threads", Title: "Go OS Threads", Units: "threads", Metric: "go_threads"},
			{ID: "go.mem_alloc", Title: "Go Allocated Memory", Units: "bytes", Metric: "go_memstats_alloc_bytes", Type: "area"},
			{ID: "go.mem_heap", Title: "Go Heap In Use", Units: "bytes", Metric: "go_memstats_heap_inuse_bytes", Type: "area"},
			{ID: "go.gc_count", Title: "Go GC Cycles", Units: "cycles/s", Metric: "go_gc_duration_seconds_count", Counter: true},
		},
	},
	{
		Name: "python_gc", Title: "Python GC", Auto: false,
		Match: []string{"python_gc_"},
		Notes: "prometheus_client Python GC collector; set profile: python_gc",
		Charts: []PromChart{
			{ID: "python.gc_objects", Title: "Python GC Objects Collected", Units: "objects/s", Metric: "python_gc_objects_collected_total", DimLabel: "generation", Counter: true},
			{ID: "python.gc_collections", Title: "Python GC Collections", Units: "collections/s", Metric: "python_gc_collections_total", DimLabel: "generation", Counter: true},
		},
	},
}

// promDedicated lists Prometheus-style integrations that already have a
// native Monitor collector — do not add a second profile.
var promDedicated = []PromCatalogEntry{
	{Name: "nginx", Kind: "dedicated", Collector: "nginx", Notes: "stub_status, not nginx_exporter"},
	{Name: "redis", Kind: "dedicated", Collector: "redis"},
	{Name: "haproxy", Kind: "dedicated", Collector: "haproxy", Notes: "stats CSV; HAProxy Prometheus endpoint stays prom.* unless you want a profile later"},
	{Name: "ceph", Kind: "dedicated", Collector: "ceph"},
	{Name: "envoy", Kind: "dedicated", Collector: "envoy"},
	{Name: "traefik", Kind: "dedicated", Collector: "traefik"},
	{Name: "coredns", Kind: "dedicated", Collector: "coredns"},
	{Name: "node_exporter", Kind: "dedicated", Collector: "cpu,mem,disk,net,proc", Notes: "use system collectors, not prom.node_* native IDs"},
	{Name: "mysql", Kind: "dedicated", Collector: "mysql"},
	{Name: "postgres", Kind: "dedicated", Collector: "postgres"},
	{Name: "elasticsearch", Kind: "dedicated", Collector: "elasticsearch"},
	{Name: "rabbitmq", Kind: "dedicated", Collector: "rabbitmq"},
	{Name: "clickhouse", Kind: "dedicated", Collector: "clickhouse"},
	{Name: "cockroachdb", Kind: "dedicated", Collector: "cockroachdb"},
	{Name: "pulsar", Kind: "dedicated", Collector: "pulsar"},
	{Name: "consul", Kind: "dedicated", Collector: "consul"},
	{Name: "docker_engine", Kind: "dedicated", Collector: "docker_engine"},
	{Name: "k8s_kubelet", Kind: "dedicated", Collector: "k8s_kubelet"},
	{Name: "k8s_kubeproxy", Kind: "dedicated", Collector: "k8s_kubeproxy"},
	{Name: "k8s_apiserver", Kind: "dedicated", Collector: "k8s_apiserver"},
}
