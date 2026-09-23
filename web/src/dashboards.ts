import type { Chart } from './api'

export interface BoardGroup { id: string; title: string; hint: string; patterns: RegExp[] }
export interface Dashboard { id: string; title: string; description: string; groups: BoardGroup[] }
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

export const dashboards: Dashboard[] = [
  { id: 'developer', title: '研发总览', description: '日常巡检：从主机资源到接口和依赖，一屏串起常见排查路径。', groups: [compute, memory, web, database, cache] },
  { id: 'services', title: '接口与网络', description: '接口变慢或访问失败时，对照网关、连接、网络与主机负载。', groups: [web, network, compute] },
  { id: 'data', title: '数据库与缓存', description: '查询变慢或队列积压时，联查数据依赖、内存和存储。', groups: [database, cache, memory, disk] },
  { id: 'containers', title: '容器工作台', description: '查看当前节点的容器、应用进程及宿主机资源。', groups: [containers, apps, compute, memory] },
  { id: 'resources', title: '资源瓶颈', description: '沿 CPU、内存、磁盘和网络逐层定位资源压力。', groups: [compute, memory, disk, network] },
  { id: 'release', title: '发布观察', description: '发布期间同步观察接口、应用进程和资源曲线；可用顶部时间范围回看趋势。', groups: [web, apps, containers, compute, memory] },
]

/** Match semantic contexts, falling back to IDs for collectors without a context. */
export function chartsForGroup(charts: Chart[], group: BoardGroup): Chart[] {
  return charts.filter(c => group.patterns.some(pattern => pattern.test(c.context || c.id)))
    .sort((a, b) => a.priority - b.priority || a.id.localeCompare(b.id))
}
