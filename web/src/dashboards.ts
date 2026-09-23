import type { Chart } from './api'

export interface BoardGroup { id: string; title: string; hint: string; patterns: RegExp[] }
export interface Dashboard { id: string; title: string; description: string; category: string; groups: BoardGroup[] }
const group = (id: string, title: string, hint: string, ...patterns: RegExp[]): BoardGroup => ({ id, title, hint, patterns })
const compute = group('compute', 'CPU 与负载', '结合 CPU、负载与进程数，观察计算压力。', /^system\.(cpu|load|processes|ctxt)$/)
const memory = group('memory', '内存与交换', '观察可用内存、换页和交换活动，定位内存压力。', /^system\.ram$/, /^mem\.(available|swap|swapio|pgfaults|writeback)$/)
const disk = group('disk', '磁盘与存储', '结合 I/O 延迟、吞吐和空间占用，判断存储瓶颈。', /^disk\.(io|ops|util|await|space|inodes)$/)
const network = group('network', '网络与连接', '对照流量、丢包与 TCP 连接，排查网络异常。', /^net\.(net|errors|drops)$/, /^ipv4\.(tcp|tcp.*|sock.*)$/, /^tcp\./)
const web = group('web', '接口与网关', '观察已采集的 HTTP 探测、网关请求和响应指标。', /^(httpcheck|http_check|portcheck|nginx|apache|haproxy|traefik|envoy)\./)
const database = group('database', '数据库', '观察查询、连接、锁等待与缓存命中情况。', /^(mysql|postgres|postgresql|pgbouncer|mssql|mongodb|sql)\./)
const cache = group('cache', '缓存与消息队列', '观察缓存内存、命中情况与队列积压。', /^(redis|memcached|rabbitmq|kafka|activemq|nats)\./)
const containers = group('containers', '容器与工作负载', '观察容器状态及 CPU、内存、网络和 I/O。', /^(docker|cgroup|containerd|k8s(?:_[a-z]+)?|kubernetes|lxc)\./)
const apps = group('apps', '应用进程', '结合应用进程资源曲线，定位资源消耗来源。', /^apps\./)

const mysql = group('mysql', 'MySQL 与连接代理', '观察查询吞吐、连接、锁与复制指标。', /^(mysql|proxysql)\./)
const postgres = group('postgres', 'PostgreSQL 与连接池', '观察事务、连接、锁与后台写入。', /^(postgres|postgresql|pgbouncer)\./)
const redis = group('redis', 'Redis 与内存缓存', '观察命中、淘汰、连接和内存使用。', /^(redis|memcached|pika)\./)
const queues = group('queues', '消息队列', '对照生产、消费和积压指标，排查消费者压力。', /^(rabbitmq|kafka|activemq|nats|beanstalk|gearman)\./)
const java = group('java', 'Java 与 Tomcat', '查看已采集的 JVM 内存、线程及 Tomcat 请求指标。', /^(tomcat|jvm|jmx)\./, /^(cassandra|logstash)\.jvm_/, /^elasticsearch\..*jvm/)
const search = group('search', '搜索与分析引擎', '观察 Elasticsearch 集群、分片和 ClickHouse 查询。', /^(elasticsearch|clickhouse)\./)
const kubernetes = group('kubernetes', 'Kubernetes 组件', '观察节点、Pod、kubelet、代理与 API Server 已采集指标。', /^k8s(?:_[a-z]+)?\./)
const dns = group('dns', 'DNS 解析', '观察 DNS 查询耗时、状态、缓存和服务端请求。', /^(dns_query|unbound|powerdns|powerdns_recursor|dnsmasq|bind_rndc)\./)
const storage = group('storage', '存储设备健康', '对照存储设备温度、寿命、错误和阵列状态。', /^(nvme|smart_log|smartd_log|zfs|zfspool|ceph|storcli|hpssa)\./)
const gpu = group('gpu', 'GPU 资源', '观察 GPU 利用率、显存、温度和功耗。', /^(nvidia_smi|amdgpu)\./)
const services = group('services', '服务状态', '观察进程守护和服务状态，结合探测定位不可用服务。', /^(supervisord|monit|systemd|systemdunits)\./)
const logs = group('logs', '日志传输管道', '对照 Fluentd 缓冲、重试与 Logstash 事件吞吐。', /^(fluentd|logstash)\./)

export const dashboards: Dashboard[] = [
  { id: 'developer', title: '研发总览', description: '日常巡检：从主机资源到接口和依赖，一屏串起常见排查路径。', category: '通用巡检', groups: [compute, memory, web, database, cache] },
  { id: 'services', title: '接口与网络', description: '接口变慢或访问失败时，对照网关、连接、网络与主机负载。', category: '通用巡检', groups: [web, network, compute] },
  { id: 'data', title: '数据库与缓存', description: '查询变慢或队列积压时，联查数据依赖、内存和存储。', category: '通用巡检', groups: [database, cache, memory, disk] },
  { id: 'containers', title: '容器工作台', description: '查看当前节点的容器、应用进程及宿主机资源。', category: '通用巡检', groups: [containers, apps, compute, memory] },
  { id: 'resources', title: '资源瓶颈', description: '沿 CPU、内存、磁盘和网络逐层定位资源压力。', category: '通用巡检', groups: [compute, memory, disk, network] },
  { id: 'release', title: '发布观察', description: '发布期间同步观察接口、应用进程和资源曲线；可用顶部时间范围回看趋势。', category: '通用巡检', groups: [web, apps, containers, compute, memory] },
  { id: 'mysql', title: 'MySQL 排障', category: '数据服务', description: '连接暴涨、查询拥堵或复制异常时，联查数据库、磁盘与内存。', groups: [mysql, disk, memory, compute] },
  { id: 'postgres', title: 'PostgreSQL 排障', category: '数据服务', description: '联查事务、连接池、锁与存储压力。', groups: [postgres, disk, memory] },
  { id: 'redis', title: 'Redis 缓存', category: '数据服务', description: '命中下降或淘汰增加时，对照缓存、主机内存和网络。', groups: [redis, memory, network] },
  { id: 'queues', title: '消息队列', category: '应用与中间件', description: '消息积压时，联查队列、应用进程和网络。', groups: [queues, apps, network, disk] },
  { id: 'java', title: 'Java / Tomcat', category: '应用与中间件', description: 'Java 应用变慢时，观察 JVM、请求线程和主机资源。', groups: [java, compute, memory, apps] },
  { id: 'search', title: '搜索与分析', category: '数据服务', description: '观察 Elasticsearch 与 ClickHouse 的查询、存储及资源压力。', groups: [search, memory, disk, compute] },
  { id: 'kubernetes', title: 'Kubernetes 工作台', category: '基础设施', description: '查看当前节点已采集的 Kubernetes 组件和主机资源。', groups: [kubernetes, compute, memory, network] },
  { id: 'dns', title: 'DNS 与连通性', category: '基础设施', description: '域名解析或访问异常时，联查 DNS、HTTP 探测和网络。', groups: [dns, web, network] },
  { id: 'storage', title: '存储与磁盘健康', category: '基础设施', description: '容量增长或 I/O 变慢时，联查磁盘使用与设备健康。', groups: [disk, storage, memory] },
  { id: 'gpu', title: 'GPU 工作台', category: '基础设施', description: 'GPU 任务运行时，观察利用率、显存、温度与主机资源。', groups: [gpu, compute, memory, disk] },
  { id: 'availability', title: '服务存活', category: '应用与中间件', description: '服务无响应时，对照守护进程、接口探测和应用资源。', groups: [services, web, apps] },
  { id: 'logs', title: '日志管道', category: '应用与中间件', description: '日志延迟或积压时，联查缓冲重试、事件吞吐和磁盘。', groups: [logs, disk, memory, network] },
]

/** Match semantic contexts, falling back to IDs for collectors without a context. */
export function chartsForGroup(charts: Chart[], group: BoardGroup): Chart[] {
  return charts.filter(c => group.patterns.some(pattern => pattern.test(c.context || c.id)))
    .sort((a, b) => a.priority - b.priority || a.id.localeCompare(b.id))
}
