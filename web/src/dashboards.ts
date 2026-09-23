import type { Chart } from './api'

export interface BoardGroup { id: string; title: string; hint: string; patterns: RegExp[] }
export interface Dashboard { id: string; title: string; description: string; category: string; checklist?: string[]; groups: BoardGroup[] }
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
const storage = group('storage', '存储设备健康', '对照存储设备温度、寿命、错误和阵列状态。', /^(nvme|smartctl|smart_log|smartd_log|md|zfs|zfspool|ceph|storcli|hpssa)\./)
const gpu = group('gpu', 'GPU 资源', '观察 GPU 利用率、显存、温度和功耗。', /^(nvidia_smi|amdgpu)\./)
const services = group('services', '服务状态', '观察进程守护和服务状态，结合探测定位不可用服务。', /^(supervisord|monit|systemd|systemdunits)\./)
const logs = group('logs', '日志传输管道', '对照 Fluentd 缓冲、重试与 Logstash 事件吞吐。', /^(fluentd|logstash)\./)

const probes = group('probes', '可用性探测', '先看 HTTP / TCP 探测状态，再对照 Ping 延迟和丢包；探测结果不等同于业务 SLA。', /^(httpcheck|portcheck)\.(status|responsetime|latency|status_code)(\.|$)/, /^ping\./)
const pressure = group('pressure', '资源等待与饱和', '对照 Linux PSI 的部分等待和全体等待，确认 CPU、内存或 I/O 是否出现争用。', /^system\.(cpu|memory|io|irq)_(some|full)_pressure(?:_stall_time)?$/)
const capacity = group('capacity', '容量与句柄余量', '观察可用内存、磁盘空间、inode 和文件句柄；曲线用于观察趋势，不预测耗尽时间。', /^disk\.(space|inodes)$/, /^mem\.(available|committed|swap)$/, /^system\.file_nr$/)
const connections = group('connections', 'TCP 与连接跟踪', '对照连接数、握手错误、重传与 conntrack 丢弃，排查连接耗尽。', /^ipv4\.tcp/, /^ipv[46]\.sockstat/, /^netfilter\.conntrack_/)
const certificates = group('certificates', 'TLS 证书剩余天数', '优先检查即将过期的 HTTPS 证书，再看 HTTP 状态；缺少证书曲线不表示证书有效。', /^httpcheck\.cert_expiry(\.|$)/)
const clock = group('clock', '时钟同步', '对照系统同步状态、Chrony / NTP 偏移和上游延迟。', /^system\.clock_/, /^(chrony|ntpd)\./)
const hardware = group('hardware', '温度与电源', '检查温度、UPS 电池、剩余供电时间与负载，区分系统负载和供电问题。', /^(sensors|apcupsd|upsd|powersupply)\./)
const files = group('files', '关键文件与目录', '检查已监控文件是否存在、多久没有更新和大小变化；这些指标不证明备份可恢复。', /^filecheck\./)

const virtualization = group('virtualization', '虚拟机与宿主平台', '观察已采集的虚拟机状态、资源与宿主平台连接；平台对象不等同于已安装 Agent 的节点。', /^(libvirt|proxmox|xen|vsphere)\./)
const proxies = group('proxies', '反向代理与负载均衡', '对照代理连接、请求、错误和后端状态；各代理仅展示已采集指标。', /^(nginx|nginxplus|nginxvts|nginxunit|tengine|haproxy|traefik|envoy)\./)
const shares = group('shares', '共享存储协议', '观察 NFS 服务端 I/O、回复缓存与 Samba 活动，再对照本机磁盘和网络。', /^(nfs|nfsd|samba)\./)
const vpn = group('vpn', 'VPN 隧道', '观察 WireGuard 对端握手时间、流量及 OpenVPN 客户端；空闲隧道不一定持续握手。', /^(wireguard|openvpn|openvpn_status_log)\./)
const mail = group('mail', '邮件队列与收取', '观察 Postfix / Exim 队列与 Dovecot 会话和认证；队列数量不代表最终投递结果。', /^(postfix|exim|dovecot)\./)
const discovery = group('discovery', '服务发现与协调', '观察 Consul 健康检查与成员、ZooKeeper 状态和请求等待。', /^(consul|zookeeper)\./)
const identity = group('identity', '目录与认证服务', '观察 OpenLDAP 操作和连接、FreeRADIUS 请求与响应；不展示用户凭据。', /^(openldap|freeradius)\./)
const devices = group('devices', '网络设备接口', '观察 SNMP 接口状态、吞吐与设备运行时间，结合探测定位链路问题。', /^snmp\.(device_[a-z_]+|trap\.[a-z_]+)(\.|$)/)

const mongodb = group('mongodb', 'MongoDB 操作与连接', '观察文档操作、连接余量、内存与网络。', /^(mongodb)\./)
const cassandra = group('cassandra', 'Cassandra 请求与压缩', '观察读写请求、失败、丢弃消息和待压缩任务。', /^(cassandra)\./)
const ceph = group('ceph', 'Ceph 集群状态', '观察集群状态、OSD、容量和客户端 I/O。', /^(ceph)\./)
const zfs = group('zfs', 'ZFS 池与 ARC', '观察池健康、空间、碎片与已采集的 ARC 缓存。', /^(zfspool|zfs)\./)
const workers = group('workers', 'Web 工作进程', '观察 PHP-FPM 队列与慢请求、uWSGI 异常和进程重启。', /^(phpfpm|phpdaemon|uwsgi)\./)
const httpCache = group('http-cache', 'HTTP 缓存代理', '观察 Varnish 命中与回源、Squid 请求和错误。', /^(varnish|squid|squidlog)\./)
const firewall = group('firewall', '规则计数与封禁', '观察 nftables 计数和 Fail2ban 封禁、失败；不能据此直接判定攻击。', /^fail2ban\./, /^netfilter\.nftables_(packets|bytes)(\.|$)/)
const bmc = group('bmc', 'IPMI / Redfish 健康', '观察 IPMI 传感器与 Redfish 系统健康，确认被监控服务器归属。', /^(ipmi|redfish)\./)

const windows = group('windows', 'Windows 调度与内核', '查看处理器队列、线程、句柄和内核内存池。', /^system\.(cpu_queue|threads|handles)$/, /^mem\.(system_pool_size|page_faults_breakdown|system_cache)$/)
const macos = group('macos', 'macOS 内存与温控', '查看内存压力、交换、温控等级与电池采样。', /^macos\./)
const android = group('android', 'Android 应用采样', '查看已接入应用使用样本中的 CPU 和流量；不代表全部应用。', /^android\./)
const mssql = group('mssql', 'SQL Server 查询与连接', '查看连接、阻塞进程、批请求、编译和缓冲区命中。', /^mssql\./)
const apache = group('apache', 'Apache 请求与 Worker', '查看忙闲 Worker、连接、请求和 scoreboard 状态。', /^apache\./)
const iis = group('iis', 'IIS 与 .NET 运行时', '查看站点请求、应用池、ASP.NET 排队和 CLR 异常与锁争用。', /^(iis|aspnet|netframework)\./)
const dhcp = group('dhcp', 'DHCP 范围与主机', '查看 dnsmasq DHCP 范围及主机计数，结合 DNS 和网络。', /^dnsmasq_dhcp\./)
const wireless = group('wireless', '无线接入状态', '查看 AP 客户端、信号、速率、重试及无线丢弃。', /^(ap|wireless)\./)

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
  { id: 'oncall', title: '值班快速巡检', category: '运维值班', description: '交接班或收到故障反馈时，先确认探测和服务状态，再看容量与负载。', groups: [probes, services, capacity, compute], checklist: ['确认探测失败是否持续，并核对目标实例。', '对照服务状态和重启变化，缩小故障范围。', '查看容量与负载曲线，继续进入对应专项看板。'] },
  { id: 'saturation', title: '资源饱和与等待', category: '运维值班', description: 'CPU 不高但服务变慢时，联查 PSI、内存交换和磁盘延迟。', groups: [pressure, compute, memory, disk], checklist: ['先看 PSI 哪类资源持续等待。', '对照同一时间的负载、换页与磁盘延迟。', '结合应用进程定位资源消耗来源，避免仅凭 CPU 下结论。'] },
  { id: 'capacity', title: '容量与耗尽风险', category: '运维值班', description: '集中观察磁盘、inode、可用内存和文件句柄的余量。', groups: [capacity, disk, memory], checklist: ['先核对挂载点的可用空间与 inode。', '再看可用内存、交换和文件句柄变化。', '切换较长时间范围观察增长；当前不提供耗尽时间预测。'] },
  { id: 'tcp', title: 'TCP 连接排障', category: '运维值班', description: '连接超时、握手失败或 NAT 异常时，联查 TCP、conntrack 和探测。', groups: [connections, network, probes], checklist: ['先确认目标端口与探测状态。', '对照 TCP 握手、重传和 conntrack 错误。', '检查网卡丢包，区分主机连接压力与链路问题。'] },
  { id: 'tls', title: '证书与入口巡检', category: '运维值班', description: '集中检查 HTTPS 证书剩余天数、入口探测和 DNS。', groups: [certificates, probes, dns], checklist: ['先看每个已采集目标的证书剩余天数。', '对照入口状态与 DNS 探测，确认影响范围。', '更新证书后再次验证目标曲线与实际入口。'] },
  { id: 'clock', title: '时钟同步巡检', category: '运维值班', description: '排查时间漂移、日志时间错位和依赖时间校验的服务异常。', groups: [clock, network], checklist: ['先确认同步状态与偏移量。', '检查 NTP / Chrony 上游延迟与可用来源。', '对照网络异常；无指标时不能判断已经同步。'] },
  { id: 'hardware', title: '硬件与电源巡检', category: '运维值班', description: '检查主机温度、UPS 电源与存储设备状态。', groups: [hardware, storage, compute], checklist: ['先看温度和 UPS 是否转电池供电。', '再看剩余供电时间及存储健康状态。', '对照负载判断是否伴随资源压力。'] },
  { id: 'files', title: '关键文件更新巡检', category: '运维值班', description: '巡检已接入的日志、产物或备份文件是否存在并持续更新。', groups: [files, capacity], checklist: ['确认预期文件或目录存在。', '核对距上次更新的时间与大小变化。', '结合任务计划人工核实；备份还需独立恢复验证。'] },
  { id: 'virtualization', title: '虚拟化平台', category: '平台服务', description: '虚拟机不可用或资源争用时，联查平台状态和采集节点的主机资源。', groups: [virtualization, compute, memory, disk], checklist: ["先按实例核对虚拟机与平台连接状态。", "对照虚拟机资源和采集节点负载，确认监控对象是否同一宿主机。", "继续检查存储延迟和资源余量，避免跨对象误判。"] },
  { id: 'proxies', title: '反向代理与负载均衡', category: '平台服务', description: '入口错误或转发变慢时，联查代理、探测和连接压力。', groups: [proxies, probes, connections, certificates], checklist: ["先看哪个代理或后端出现状态、请求或错误变化。", "对照端口和 HTTP 探测，核对目标服务。", "检查连接压力与证书剩余天数，缩小入口故障范围。"] },
  { id: 'shares', title: 'NFS / Samba 共享存储', category: '平台服务', description: '共享目录访问慢时，联查协议活动、网络、磁盘和空间。', groups: [shares, network, disk, capacity], checklist: ["先确定受影响的共享服务和对应主机。", "对照协议 I/O 与网卡流量、磁盘延迟。", "检查挂载点余量；无数据时不能判断远程挂载健康。"] },
  { id: 'vpn', title: 'VPN 隧道巡检', category: '平台服务', description: '远程访问异常时，联查 WireGuard / OpenVPN、网络与探测。', groups: [vpn, network, probes], checklist: ["先核对已采集隧道、对端及活跃客户端。", "对照握手距今时间和流量；空闲对端不直接判离线。", "验证目标探测和底层网络，区分隧道与业务故障。"] },
  { id: 'mail', title: '邮件服务巡检', category: '平台服务', description: '邮件发送积压或收取异常时，联查队列、会话和磁盘空间。', groups: [mail, capacity, network, probes], checklist: ["先看邮件队列是否持续增长及收取会话变化。", "检查磁盘余量、网络与端口探测。", "结合邮件服务日志核实投递结果，不能以队列为空证明投递成功。"] },
  { id: 'discovery', title: '服务发现与协调', category: '平台服务', description: '服务注册或协调异常时，联查 Consul、ZooKeeper 与网络。', groups: [discovery, network, memory, probes], checklist: ["先看健康检查、成员与服务端状态变化。", "对照请求等待、耗时和连接数。", "检查底层网络和可用内存，并人工核对集群拓扑。"] },
  { id: 'identity', title: '目录与认证巡检', category: '平台服务', description: '登录或认证请求异常时，联查 LDAP、RADIUS、时间与网络。', groups: [identity, clock, network, probes], checklist: ["先确认 LDAP / RADIUS 连接和请求响应变化。", "核对时钟同步、网络与服务端口探测。", "结合服务日志和权限配置核实原因，指标不直接证明账户被攻击。"] },
  { id: 'devices', title: '网络设备巡检', category: '平台服务', description: '交换机或路由器链路异常时，联查 SNMP 接口状态、流量与探测。', groups: [devices, probes, network], checklist: ["先按设备和接口核对运行状态及设备重启迹象。", "对照接口流量、陷阱事件和端到端探测。", "确认所选节点网卡是否属于故障路径，避免混淆采集机与设备。"] },
  { id: 'mongodb', title: 'MongoDB 专项', category: '数据服务', description: '观察文档操作、连接余量、内存与网络。', groups: [mongodb, memory, disk, network], checklist: ["先核对操作速率和连接余量变化。", "对照数据库内存与主机磁盘延迟。", "结合慢查询日志核实原因；这些指标不提供单条查询耗时。"] },
  { id: 'cassandra', title: 'Cassandra 专项', category: '数据服务', description: '观察读写请求、失败、丢弃消息和待压缩任务。', groups: [cassandra, memory, disk, network], checklist: ["先看请求失败和消息丢弃是否增加。", "核对待压缩任务与磁盘空间和吞吐。", "结合 JVM 内存与网络确定排查方向。"] },
  { id: 'ceph', title: 'Ceph 集群存储', category: '基础设施', description: '观察集群状态、OSD、容量和客户端 I/O。', groups: [ceph, disk, network], checklist: ["先看集群状态与 OSD 的 up / down、in / out。", "核对集群容量和客户端吞吐变化。", "确认本机是否是相关存储节点，再联查磁盘与网络。"] },
  { id: 'zfs', title: 'ZFS 存储池', category: '基础设施', description: '观察池健康、空间、碎片与已采集的 ARC 缓存。', groups: [zfs, memory, disk], checklist: ["先看存储池是否降级或不可用。", "核对池空间与碎片，再看 ARC 和内存。", "结合磁盘状态排查；健康状态不替代数据校验。"] },
  { id: 'workers', title: 'PHP / uWSGI 工作进程', category: '应用与中间件', description: '观察 PHP-FPM 队列与慢请求、uWSGI 异常和进程重启。', groups: [workers, proxies, compute, memory], checklist: ["先看请求排队、慢请求与异常变化。", "对照进程重启、CPU 与内存压力。", "核对反向代理与应用日志，避免仅靠增加进程数处理。"] },
  { id: 'http-cache', title: 'HTTP 缓存与回源', category: '应用与中间件', description: '观察 Varnish 命中与回源、Squid 请求和错误。', groups: [httpCache, proxies, network, memory], checklist: ["先看缓存命中和请求量是否同时变化。", "核对回源请求、连接失败和网络流量。", "结合缓存规则与源站日志核实原因，指标不代替配置检查。"] },
  { id: 'firewall', title: '防火墙与封禁观察', category: '基础设施', description: '观察 nftables 计数和 Fail2ban 封禁、失败；不能据此直接判定攻击。', groups: [firewall, connections, probes], checklist: ["先确认规则或 jail 对应的业务与目标。", "对照封禁、失败计数和 conntrack 状态。", "核对服务探测与日志，避免将正常流量误判为攻击。"] },
  { id: 'bmc', title: '服务器带外管理', category: '平台服务', description: '观察 IPMI 传感器与 Redfish 系统健康，确认被监控服务器归属。', groups: [bmc, hardware, storage], checklist: ["先核对服务器与传感器的健康状态。", "对照温度、电源和存储设备指标。", "确认指标来自同一服务器；采集机资源不等同于远程服务器资源。"] },
  { id: 'windows', title: 'Windows 主机', category: '操作系统', description: '查看处理器队列、线程、句柄和内核内存池。', groups: [windows, compute, memory, disk], checklist: ["先看处理器队列与线程、句柄是否持续增长。", "对照内核内存池、分页和磁盘延迟。", "缺少 Perflib 指标时先确认采集权限与计数器，不能据空白判断正常。"] },
  { id: 'macos', title: 'macOS 主机', category: '操作系统', description: '查看内存压力、交换、温控等级与电池采样。', groups: [macos, compute, memory, apps], checklist: ["先看内存压力和交换使用。", "对照温控等级与 CPU 负载，确认是否伴随资源压力。", "结合应用进程定位来源；台式机或权限不足时可能没有电池数据。"] },
  { id: 'android', title: 'Android 应用资源', category: '操作系统', description: '查看已接入应用使用样本中的 CPU 和流量；不代表全部应用。', groups: [android, compute, memory, network], checklist: ["先确认应用使用样本覆盖哪些应用。", "对照应用 CPU 和流量变化与主机资源。", "没有应用样本时先核实采集输入，不能视为应用没有活动。"] },
  { id: 'mssql', title: 'SQL Server 专项', category: '数据服务', description: '查看连接、阻塞进程、批请求、编译和缓冲区命中。', groups: [mssql, disk, memory, compute], checklist: ["先看阻塞进程和连接变化。", "对照批请求、编译与缓冲区命中。", "结合数据库执行计划和等待信息核实原因，当前不展示单条 SQL。"] },
  { id: 'apache', title: 'Apache 工作线程', category: '应用与中间件', description: '查看忙闲 Worker、连接、请求和 scoreboard 状态。', groups: [apache, probes, compute, memory], checklist: ["先看忙闲 Worker 与连接状态。", "对照请求量、流量和 HTTP 探测。", "结合服务日志核实耗时来源，避免仅增加线程上限。"] },
  { id: 'iis', title: 'IIS / .NET 应用', category: '应用与中间件', description: '查看站点请求、应用池、ASP.NET 排队和 CLR 异常与锁争用。', groups: [iis, probes, compute, memory], checklist: ["先按站点或应用池定位异常。", "对照 ASP.NET 队列、重启和 CLR 异常、锁等待。", "检查主机资源与应用日志，计数器缺失时先核实采集条件。"] },
  { id: 'dhcp', title: 'DHCP 与地址分配', category: '平台服务', description: '查看 dnsmasq DHCP 范围及主机计数，结合 DNS 和网络。', groups: [dhcp, dns, network], checklist: ["先核对 DHCP 范围与主机计数是否变化。", "检查 DNS 和网络状态，确认影响范围。", "地址池剩余量和租约冲突需另行核实，当前计数不等于可用地址数。"] },
  { id: 'wireless', title: '无线 AP 与客户端', category: '平台服务', description: '查看 AP 客户端、信号、速率、重试及无线丢弃。', groups: [wireless, network, probes], checklist: ["先定位受影响 AP 或无线接口。", "对照客户端数、信号、重试和吞吐。", "用端到端探测验证影响；平均信号不能代表每个客户端体验。"] },
]

/** Match semantic contexts, falling back to IDs for collectors without a context. */
export function chartsForGroup(charts: Chart[], group: BoardGroup): Chart[] {
  return charts.filter(c => group.patterns.some(pattern => pattern.test(c.context || c.id)))
    .sort((a, b) => a.priority - b.priority || a.id.localeCompare(b.id))
}
