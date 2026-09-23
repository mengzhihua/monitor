# plugins.d 外部采集器协议

`monitord` 可以把任意语言写的程序作为采集器运行：进程把指标按下面的**文本协议**写到 stdout，agent 逐行解析并写入 Registry / TSDB，随后与内置采集器一样出现在 Dashboard、`/api/v1/*`、`/metrics` 和告警规则中。协议语义与 Netdata 外部插件协议兼容，多数现成的 Netdata shell/Python 插件无需修改即可运行。

## 1. 进程生命周期

| 阶段 | 行为 |
| --- | --- |
| 发现 | 启动时扫描 `plugins.dir`（默认 `plugins.d/`，相对配置文件所在目录）中**可执行**的 `*.plugin` 文件（Windows 额外识别 `*.plugin.exe/.bat/.cmd`），插件名 = 去掉 `.plugin` 的文件名；`plugins.list` 中的显式声明优先级更高，同名覆盖 |
| 启动 | `<command> <update_every> [args...]`，工作目录为 agent 的当前目录；环境变量 `MONITOR_UPDATE_EVERY`、`MONITOR_HOSTNAME`、`MONITOR_PLUGIN`、`NETDATA_UPDATE_EVERY`（兼容） |
| 输出 | stdout：协议；stderr：逐行记入 agent 日志（`component=plugins`）；stdin 关闭 |
| 看门狗 | `timeout` 秒（默认 `max(10×update_every, 60)`）无任何输出 → 结束进程并重启 |
| 退出 | 进程退出 / 崩溃 / 被看门狗结束 / 发送 `EXIT` → 等待后重启；退避 1s→2s→…→60s，运行足够久（>5 分钟）后重置 |
| 禁用 | 发送 `DISABLE`（例如依赖不存在）→ 进程结束且**不再重启**，状态 `disabled`；`plugins.disabled` 或 `list[].disabled: true` 可在配置层禁用 |
| 停止 | agent 关闭时向插件的进程组发送 SIGTERM（Unix），3s 后强制回收管道；shell 插件建议 `exec` 长驻子进程或自己处理信号 |
| 状态 | `GET /api/v1/collectors` → `plugins[]`：`state`（starting/running/waiting/stopped/disabled/failed）、`pid`、`restarts`、`error`、`stats`（行数/样本数/图表数/协议错误数与最后一次错误/最后数据时间）、`variables` |

## 2. 命令

一行一条命令，空白分隔，字段可用单引号或双引号包裹（可包含空格）；空行与 `#` 开头行忽略。命令不区分大小写。

### 定义图表

```
CHART type.id [name [title [units [family [context [charttype [priority [update_every [options [plugin [module]]]]]]]]]]]
DIMENSION id [name [algorithm [multiplier [divisor [options]]]]]
CLABEL key value [source]
CLABEL_COMMIT
```

- `type.id` 是图表唯一 ID（也用作 `/api/v1/data?chart=`）；`family` 默认取 `type`；`context` 默认 = ID（告警模板 `on:` 按 context 匹配）。
- `charttype`：`line`（默认）/ `area` / `stacked`。`priority` 越小越靠前（默认 100000）。`plugin`/`module` 只用于展示，chart 的 `plugin` 字段为 `<插件名>[/<plugin 字段>]`。
- `DIMENSION` 属于其上方最近的 `CHART`。`algorithm`：`absolute`（默认）/ `incremental`（计数器，agent 计算每秒增量并处理回绕）/ `percentage-of-absolute-row` / `percentage-of-incremental-row`。最终值 = 算法输出 × `multiplier` ÷ `divisor`（默认 1/1）。`options` 含 `hidden` 时 Dashboard 不绘制但 API 可查。
- `CLABEL` 给最近的 `CHART` 添加标签，`CLABEL_COMMIT` 提交。图表在第一条非 `DIMENSION/CLABEL/CLABEL_COMMIT` 命令到达时注册。
- 插件重启后重新 `CHART` 同一个 ID 会复用已有图表（新维度追加，历史数据连续）。

### 发送数据

```
BEGIN type.id [microseconds]
SET id = value
SET id =            # 该维度这一轮无数据（留空）
END
FLUSH               # 丢弃当前 BEGIN 块
```

- `value` 为十进制整数或浮点数。`microseconds` 仅为兼容，采集时间戳取 agent 收到 `BEGIN` 的时间。
- 一轮 `BEGIN…END` 完成后才写入 TSDB 并推送 WebSocket；`END` 时没有任何 `SET` 则本轮跳过。

### 其他

```
VARIABLE [HOST|CHART|GLOBAL] name = value   # 发布数值变量（状态 API 可见，供后续告警引用）
DISABLE                                     # 让 agent 永久停用本插件
EXIT                                        # 正常退出（agent 会按退避策略重启）
```

`LABEL` 立刻写入当前图表标签（不必再 `CLABEL_COMMIT`）。`HOST_LABEL` 写入主机标签。`OVERWRITE` 与 `CHART` 参数相同，但替换已有图表的标题、单位和维度。`FUNCTION` 只登记名称，出现在采集器状态里；agent 不把 stdin 交给插件，因此不会真正调用。`HOST_DEFINE` / `HOST_DEFINE_END` / `REPORT_JOB_STATUS` 仍被接受并忽略。其他未知命令、`BEGIN` 未定义的图表、`SET` 未定义的维度、`SET/END` 不在 `BEGIN` 内等都会计入 `stats.errors` 并跳过该行，**不会**中断插件。单行上限 64 KiB。

## 3. 最小示例（shell）

```sh
#!/bin/sh
every="${1:-1}"
echo "CHART example.random '' 'Random Numbers' 'value' example example.random line 90000 $every"
echo "DIMENSION random1 'random 1' absolute 1 1"
echo "DIMENSION random2 'random 2' absolute 1 1"
while true; do
  echo "BEGIN example.random"
  echo "SET random1 = $(( $(od -An -N2 -tu2 /dev/urandom) % 100 ))"
  echo "SET random2 = $(( $(od -An -N2 -tu2 /dev/urandom) % 100 ))"
  echo "END"
  sleep "$every"
done
```

保存为 `plugins.d/myplugin.plugin`，`chmod +x`，重启 `monitord` 即可；`curl localhost:19999/api/v1/collectors | jq .plugins` 查看状态，`/api/v1/data?chart=example.random&after=-60` 查数据。

仓库内附带两个可直接运行的示例：

- [`plugins.d/example.plugin`](../plugins.d/example.plugin)：shell，随机数 + `incremental` 计数器 + `CLABEL`；
- [`plugins.d/python_example.plugin.py`](../plugins.d/python_example.plugin.py)：Python，展示按行 flush；通过 `plugins.list` 声明启用（见 `monitor.example.yaml`）。

## 4. 编写建议

1. **每行立即 flush**（Python `flush=True`，Go `bufio.Writer.Flush()`）——agent 按行读取，缓冲会触发看门狗。
2. 以 `update_every`（第一个参数）为周期发数据；每轮所有图表各一个 `BEGIN…END`。
3. 依赖不存在（缺文件、无权限、服务未运行）时打印原因到 stderr 并输出 `DISABLE`，而不是反复退出。
4. 计数器类指标用 `incremental`，把原始累计值 `SET` 出来，由 agent 求速率。
5. 需要密钥/参数时用 `plugins.list[].env` 或 `args`，不要写进插件文件。
