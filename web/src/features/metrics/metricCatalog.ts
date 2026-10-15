import type { Chart } from '../../api'

/** One catalog owns discovery semantics; rendering and pagination use this same index. */
export const metricDomains = [
  { id: 'host', title: '主机资源', description: 'CPU、内存、负载与系统状态' },
  { id: 'network', title: '网络', description: '接口流量、连接、丢包与网络探测' },
  { id: 'storage', title: '存储', description: '磁盘容量、I/O、阵列与文件系统' },
  { id: 'devices', title: '设备', description: 'SNMP、UPS、打印机、BMC 与硬件传感器' },
  { id: 'applications', title: '应用与服务', description: '进程、Web 服务、接口与运行时' },
  { id: 'middleware', title: '数据库与中间件', description: '数据库、缓存、消息队列与搜索引擎' },
  { id: 'containers', title: '容器与虚拟化', description: '容器、Kubernetes、虚拟机与宿主平台' },
  { id: 'other', title: '其他指标', description: '自定义采集与尚未归类的指标' },
] as const

export type MetricDomain = typeof metricDomains[number]['id']
export interface IndexedMetric { chart: Chart; domain: MetricDomain; search: string }
const domainsByPrefix: Array<[MetricDomain, RegExp]> = [
  ['devices', /^(snmp(?:_.*)?|redfish|ipmi|ipmi_sensor|sensors|hwmon|apcupsd|upsd|nut|powersupply|battery|printer|cups|bluetooth|nvidia_smi|amdgpu)$/],
  ['containers', /^(docker|cgroup|cgroups|containerd|k8s(?:_.*)?|kubernetes|lxc|libvirt|proxmox|xen|xenstat|vsphere)$/],
  ['middleware', /^(mysql|postgres|postgresql|pgbouncer|proxysql|mssql|mongodb|sql|redis|memcached|pika|rabbitmq|kafka|activemq|nats|beanstalk|gearman|elasticsearch|opensearch|clickhouse|cassandra|couchdb|couchbase|consul|etcd|zookeeper)$/],
  ['storage', /^(disk|diskspace|filesystem|nvme|smartctl|smart_log|smartd_log|md|zfs|zfspool|ceph|storcli|hpssa|btrfs|nfs|nfsd|samba|filecheck)$/],
  ['network', /^(net|network|ipv4|ipv6|tcp|udp|icmp|ping|netfilter|nftables|conntrack|ipvs|wireguard|openvpn|dns_query|unbound|powerdns|powerdns_recursor|dnsmasq|dnsmasq_dhcp|bind_rndc|portcheck|tc|qos|ap|wireless|wifi|fail2ban)$/],
  ['applications', /^(apps|process|processes|nginx|nginxplus|nginxvts|nginxunit|apache|haproxy|traefik|envoy|tengine|httpcheck|http_check|jvm|jmx|tomcat|phpfpm|phpdaemon|uwsgi|varnish|squid|squidlog|python|go|nodejs|dotnet|iis|aspnet|netframework|android|supervisord|monit|systemd|systemdunits|fluentd|logstash|postfix|exim|dovecot|openldap|freeradius)$/],
  ['host', /^(system|cpu|mem|memory|load|swap|uptime|os|kernel|numa|interrupts|softirqs|entropy|logind|chrony|ntpd|windows|wmi|macos|freebsd|proc)$/],
]

export function metricDomain(chart: Chart): MetricDomain {
  // Context is stable across per-instance chart IDs; fall back to collector metadata.
  for (const value of [chart.context, chart.id, chart.module, chart.plugin]) {
    const prefix = (value || '').toLowerCase().split(/[.\s/:]/)[0] || ''
    for (const [domain, pattern] of domainsByPrefix) if (pattern.test(prefix)) return domain
    // Legacy IDs such as disk_await.sda encode the metric in their first segment.
    const legacyPrefix = prefix.split('_')[0] || ''
    for (const [domain, pattern] of domainsByPrefix) if (pattern.test(legacyPrefix)) return domain
  }
  return 'other'
}

export function indexMetrics(charts: Chart[]): IndexedMetric[] {
  const order = new Map<string, number>(metricDomains.map((domain, index) => [domain.id, index]))
  return charts.map(chart => ({
    chart,
    domain: metricDomain(chart),
    search: [chart.id, chart.title, chart.family, chart.context, chart.module, chart.plugin,
      ...Object.entries(chart.labels || {}).flat()].join(' ').toLocaleLowerCase(),
  })).sort((a, b) => (order.get(a.domain)! - order.get(b.domain)!)
    || a.chart.priority - b.chart.priority || a.chart.id.localeCompare(b.chart.id))
}

export function metricMatches(metric: IndexedMetric, query: string): boolean {
  const terms = query.trim().toLocaleLowerCase().split(/\s+/).filter(Boolean)
  return terms.every(term => metric.search.includes(term))
}
