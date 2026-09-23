import type { Chart, Dimension } from './api'
import { dashboards } from './dashboards'

// Keep explanations shared by the all-metrics view and every preset/personal board.
const groups = [...new Map(dashboards.flatMap(b => b.groups).map(g => [g.id, g])).values()].reverse()
const descriptions: Record<string, string> = {
  'system.cpu': 'CPU 时间在用户程序、内核、等待等状态之间的占比。持续繁忙时结合负载和应用进程判断原因；不能只凭一个采样点判断性能。',
  'cpu.cpu': '单个逻辑 CPU 的时间使用构成。用于发现单核繁忙或负载不均，应与全机 CPU 和进程活动对照。',
  'system.uptime': '系统自启动以来的运行时间，突然回落可能表示重启；应结合主机身份和采样连续性确认。',
  'mem.kernel': '内核占用的内存构成，结合应用内存与可用内存观察系统资源消耗。',
  'mem.writeback': '等待写回或正在写回存储的内存，持续增长时结合磁盘 I/O 与延迟排查。',
  'mem.committed': '已承诺的虚拟地址空间，包含尚未访问的预留，不等于当前驻留的物理内存。默认启发式超卖不会按 CommitLimit 拒绝分配，所以这个数可以高于物理内存。',
  'disk.avgsz': '每次已完成 I/O 的平均数据量，用于区分大块顺序传输与小块访问，不能单独代表性能优劣。',
  'net.packets': '网卡接收与发送的数据包速率，结合吞吐、错误与丢包判断链路活动。',
  'system.load': '过去 1、5、15 分钟的系统平均负载。它不是 CPU 使用率；需结合 CPU 核数、运行队列及 I/O 等待判断压力。',
  'system.ram': '物理内存的使用构成。缓存通常可部分回收，空闲内存少不一定是内存不足，需结合可用内存与交换活动判断。',
  'mem.available': '系统估计可供新任务使用、且无需交换的内存，包含可回收的部分缓存；比单独查看空闲内存更适合观察余量。',
  'mem.swap': '交换空间的已用和剩余容量。已有交换占用不一定表示当前正在频繁换页。',
  'mem.swapio': '内存与交换空间之间的数据交换活动。持续换入换出时，应结合可用内存和磁盘延迟排查。',
  'mem.pgfaults': '访问内存页时发生的缺页事件。缺页不等于程序错误；主要缺页可能需要读取存储，轻微缺页通常不需要磁盘读取。',
  'system.processes': '系统进程状态计数，结合 CPU 负载观察运行或阻塞的任务。',
  'system.ctxt': 'CPU 在任务之间切换执行上下文的频率。升高可能来自并发或调度活动，不单独代表故障。',
  'system.file_nr': '系统文件句柄使用情况。持续增长时结合配置上限和应用行为检查资源余量。',
  'disk.io': '磁盘读写吞吐，即单位时间传输的数据量；吞吐高低不直接等于磁盘延迟。',
  'disk.ops': '磁盘每秒完成的读写操作数（IOPS），结合吞吐与请求延迟判断工作负载。',
  'disk.await': '磁盘 I/O 请求的平均等待时间，通常包含排队和处理时间；应区分读写并观察持续变化。',
  'disk.util': '采样期间磁盘处于处理 I/O 状态的时间占比；不能单靠这个数判断并行存储的性能上限。',
  'disk.space': '文件系统空间使用与剩余情况。请核对挂载点，避免把不同磁盘或远程挂载混为一谈。',
  'disk.inodes': '文件系统 inode 使用情况。即使还有磁盘空间，inode 耗尽也可能导致无法创建文件。',
  'net.net': '网络接口接收与发送的流量。先核对接口和单位，再结合丢包、错误和连接指标排查。',
  'net.errors': '网络接口收发错误计数或速率。它不是业务请求错误率，需结合链路与接口状态判断。',
  'net.drops': '网络接口丢弃的数据包计数或速率。丢弃原因需结合队列、资源及接口配置检查。',
}
const unitNames: Record<string, string> = {
  '% of time working': '处于工作状态的时间百分比', 'milliseconds/operation': '每次操作的平均毫秒数', 'KiB/operation': '每次操作的平均 KiB 数', switches: '上下文切换次数', drops: '丢弃次数',
  '%': '百分比', percentage: '百分比', percent: '百分比', seconds: '秒', s: '秒', ms: '毫秒', milliseconds: '毫秒', us: '微秒', microseconds: '微秒', ns: '纳秒', days: '天',
  bytes: '字节', B: '字节', KiB: '千二进制字节（1 KiB = 1024 字节）', MiB: '兆二进制字节（1 MiB = 1024 KiB）', GiB: '吉二进制字节（1 GiB = 1024 MiB）',
  KB: '千字节', MB: '兆字节', GB: '吉字节', kilobits: '千比特', megabits: '兆比特',
  requests: '请求数', queries: '查询数', operations: '操作数', packets: '数据包数', connections: '连接数', sessions: '会话数', users: '用户数', threads: '线程数', processes: '进程数', files: '文件数', jobs: '任务数', events: '事件数', errors: '错误数', calls: '调用次数', faults: '缺页次数', inodes: 'inode 数量', load: '平均负载（不是百分比）',
  celsius: '摄氏度', Celsius: '摄氏度', watts: '瓦特', rpm: '每分钟转数', state: '状态值，具体编码需核对该采集器定义', status: '状态值，具体编码需核对该采集器定义', value: '采集器提供的数值，实际量纲需核对采集配置',
}
export function unitHelp(unit: string): string {
  if (unitNames[unit]) return `单位：${unitNames[unit]}（${unit}）。`
  if (unit.endsWith('/s')) {
    const base = unit.slice(0, -2)
    return `单位：每秒${unitNames[base] || base}（${unit}），表示速率。`
  }
  return `原始单位：${unit || '未提供'}。保留采集器量纲，不自动换算为百分比或业务成功率。`
}
export function metricHelp(chart: Chart): string {
  const context = chart.context || chart.id
  const group = groups.find(g => g.patterns.some(p => p.test(context)))
  const description = descriptions[context] || (group ? `${group.title}相关指标。${group.hint}` : '此图展示采集器上报的指标变化；尚无专用释义，请结合原始名称、维度和采集器定义理解，不能据此直接判断服务健康。')
  return `${description}\n${unitHelp(chart.units)}\n原始指标：${chart.title}（${chart.id}）。采样间隔：${Math.max(1, chart.update_every || 1)} 秒。\n悬浮曲线查看对应时间的数值；“-”表示未选中样本或该位置无数据，不等于 0。`
}
const dimensionNames: Record<string, string> = {
  used: '已使用部分', free: '空闲部分', available: '可用部分', avail: '可用部分', cached: '缓存部分', buffers: '缓冲区部分',
  read: '读取', reads: '读取', write: '写入', writes: '写入', received: '接收', sent: '发送', in: '进入', out: '离开', incoming: '入站', outgoing: '出站',
  active: '活跃状态', idle: '空闲状态', pending: '等待处理', running: '运行状态', blocked: '阻塞状态', stopped: '停止状态',
  success: '成功', failed: '失败', errors: '错误', dropped: '丢弃', total: '合计', count: '数量', hits: '命中', misses: '未命中',
  load1: '过去 1 分钟的平均负载', load5: '过去 5 分钟的平均负载', load15: '过去 15 分钟的平均负载',
}
const cpuNames: Record<string, string> = { user: '用户程序执行时间占比', system: '内核执行时间占比', nice: '调整过优先级的用户程序执行时间占比', iowait: '等待 I/O 完成的 CPU 时间占比，不等于磁盘利用率', irq: '处理硬件中断的时间占比', softirq: '处理软件中断的时间占比', steal: '虚拟 CPU 等待宿主机调度的时间占比', idle: 'CPU 空闲时间占比', guest: '运行虚拟机的时间占比', guest_nice: '运行调整过优先级虚拟机的时间占比' }
export function dimensionHelp(chart: Chart, dim: Dimension): string {
  const context = chart.context || chart.id
  const meaning = (['system.cpu', 'cpu.cpu'].includes(context) ? cpuNames[dim.id] : undefined) || dimensionNames[dim.id]
  return `${meaning || '该采集器定义的序列或实例，名称不直接代表健康状态'}。\n维度：${dim.name || dim.id}（${dim.id}）。${unitHelp(chart.units)}\n数值对应鼠标选中的采样时刻；“-”不等于 0。${chart.chart_type === 'stacked' ? '堆叠图的图例显示该维度自身的值，不是累计高度。' : ''}`
}
