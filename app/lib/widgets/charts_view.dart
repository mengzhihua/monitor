import 'dart:async';

import 'package:fl_chart/fl_chart.dart';
import 'package:flutter/material.dart';

import '../api/client.dart';
import '../api/models.dart';
import '../state/chart_series.dart';

/// Chart list grouped by family, with history from `/api/v1/data` and live
/// updates from the WebSocket for the charts currently on screen.
class ChartsView extends StatefulWidget {
  const ChartsView({
    super.key,
    required this.client,
    this.node,
    this.active = true,
  });

  final ApiClient client;
  final String? node;
  final bool active;

  @override
  State<ChartsView> createState() => _ChartsViewState();
}

class _ChartsViewState extends State<ChartsView> {
  List<Chart> _charts = const [];
  String? _error;
  bool _loading = true;
  String _filter = '';
  int _windowSeconds = 300;
  final _series = <String, ChartSeries>{};
  final _activeCharts = <String, Chart>{};
  final _activeOwners = <String, Object>{};
  final _historyRequests = <String, Object>{};
  LiveSubscription? _live;
  StreamSubscription<LiveSample>? _liveSub;
  Set<String> _subscribed = {};
  Timer? _reconnect;
  Timer? _refresh;
  Timer? _subscriptionUpdate;
  int _loadGeneration = 0;
  int _sourceGeneration = 0;

  @override
  void initState() {
    super.initState();
    if (widget.active) _load();
    _refresh = Timer.periodic(const Duration(seconds: 30), (_) {
      if (widget.active) _load();
    });
  }

  @override
  void didUpdateWidget(covariant ChartsView oldWidget) {
    super.didUpdateWidget(oldWidget);
    if (oldWidget.client == widget.client && oldWidget.node == widget.node) {
      if (oldWidget.active != widget.active) {
        if (widget.active) {
          _load();
        } else {
          _subscriptionUpdate?.cancel();
          _stopLive();
          for (final series in _series.values) {
            series.loaded = false;
          }
        }
      }
      return;
    }
    ++_sourceGeneration;
    ++_loadGeneration;
    _subscriptionUpdate?.cancel();
    _stopLive();
    _activeCharts.clear();
    _activeOwners.clear();
    _historyRequests.clear();
    for (final series in _series.values) {
      series.dispose();
    }
    _series.clear();
    _charts = const [];
    _error = null;
    _loading = true;
    if (widget.active) _load();
  }

  @override
  void dispose() {
    ++_loadGeneration;
    ++_sourceGeneration;
    _refresh?.cancel();
    _subscriptionUpdate?.cancel();
    _stopLive();
    for (final series in _series.values) {
      series.dispose();
    }
    super.dispose();
  }

  Future<void> _load() async {
    final generation = ++_loadGeneration;
    try {
      final list = await widget.client.charts(node: widget.node);
      if (!mounted || generation != _loadGeneration) return;
      final byId = {for (final chart in list) chart.id: chart};
      _activeCharts.removeWhere((id, _) => !byId.containsKey(id));
      _activeOwners.removeWhere((id, _) => !byId.containsKey(id));
      for (final id in _activeCharts.keys.toList()) {
        _activeCharts[id] = byId[id]!;
      }
      setState(() {
        _charts = list;
        _error = null;
        _loading = false;
      });
      _refreshActiveHistory();
      _queueSubscriptionUpdate();
      // Cards detach their listeners during the next frame before eviction.
      WidgetsBinding.instance.addPostFrameCallback((_) {
        if (!mounted || generation != _loadGeneration) return;
        for (final id in _series.keys.toList()) {
          if (!byId.containsKey(id)) _series.remove(id)?.dispose();
        }
      });
    } catch (e) {
      if (!mounted || generation != _loadGeneration) return;
      setState(() {
        _error = e.toString();
        _loading = false;
      });
      _refreshActiveHistory();
    }
  }

  Iterable<Chart> get _visible {
    final f = _filter.trim().toLowerCase();
    return _charts.where(
      (c) =>
          f.isEmpty ||
          c.id.toLowerCase().contains(f) ||
          c.title.toLowerCase().contains(f) ||
          c.family.toLowerCase().contains(f),
    );
  }

  void _stopLive() {
    _reconnect?.cancel();
    _reconnect = null;
    final subscription = _liveSub;
    final live = _live;
    _liveSub = null;
    _live = null;
    _subscribed = {};
    unawaited(subscription?.cancel());
    unawaited(live?.close());
  }

  void _syncLive() {
    if (!mounted) return;
    final ids = widget.active && _windowSeconds <= 1200
        ? _activeCharts.keys.toSet()
        : <String>{};
    if (ids.isEmpty) {
      _stopLive();
      return;
    }
    if (_reconnect?.isActive ?? false) return;
    if (_live != null) {
      if (ids.length != _subscribed.length || !ids.containsAll(_subscribed)) {
        _live!.setCharts(ids.toList());
        _subscribed = ids;
      }
      return;
    }
    final live = widget.client.live(charts: ids.toList(), node: widget.node);
    _live = live;
    _subscribed = ids;
    _liveSub = live.samples.listen(
      (sample) {
        if (_live != live || !_activeCharts.containsKey(sample.chart)) return;
        final series = _series[sample.chart];
        if (series != null &&
            series.window == _windowSeconds &&
            series.loaded) {
          if (series.add(sample.t, sample.values)) series.notify();
        }
      },
      onError: (_) => _scheduleReconnect(live),
      onDone: () => _scheduleReconnect(live),
    );
  }

  void _scheduleReconnect(LiveSubscription live) {
    if (!mounted || _live != live) return;
    _stopLive();
    _reconnect = Timer(const Duration(seconds: 3), () {
      _reconnect = null;
      _syncLive();
      _refreshActiveHistory();
    });
  }

  void _queueSubscriptionUpdate() {
    _subscriptionUpdate?.cancel();
    if (!widget.active) return;
    _subscriptionUpdate = Timer(const Duration(milliseconds: 50), () {
      if (!mounted) return;
      for (final chart in _activeCharts.values) {
        unawaited(_fetchHistory(chart));
      }
      _syncLive();
    });
  }

  void _setChartActive(Chart chart, bool active, Object owner) {
    if (active) {
      _activeCharts[chart.id] = chart;
      _activeOwners[chart.id] = owner;
    } else if (identical(_activeOwners[chart.id], owner)) {
      _activeCharts.remove(chart.id);
      _activeOwners.remove(chart.id);
      // Returning to the viewport should fill the time spent unsubscribed.
      _series[chart.id]?.loaded = false;
    }
    _queueSubscriptionUpdate();
  }

  void _refreshActiveHistory() {
    for (final chart in _activeCharts.values) {
      unawaited(_fetchHistory(chart, refresh: true));
    }
  }

  ChartSeries _seriesFor(Chart chart) {
    final series = _series.putIfAbsent(
      chart.id,
      () => ChartSeries(chart, _windowSeconds),
    );
    if (_definition(series.chart) != _definition(chart)) series.loaded = false;
    series.chart = chart;
    return series;
  }

  String _definition(Chart chart) =>
      '${chart.type}|${chart.updateEvery}|${chart.dimensions.map((d) => '${d.id}:${d.hidden}').join(',')}';

  Future<void> _fetchHistory(Chart chart, {bool refresh = false}) async {
    if (!widget.active) return;
    final series = _seriesFor(chart);
    if ((!refresh && series.loaded && series.window == _windowSeconds) ||
        _historyRequests.containsKey(chart.id)) {
      return;
    }
    final request = Object();
    _historyRequests[chart.id] = request;
    final requestedWindow = _windowSeconds;
    final source = _sourceGeneration;
    final definition = _definition(chart);
    bool isCurrent() =>
        mounted &&
        widget.active &&
        source == _sourceGeneration &&
        requestedWindow == _windowSeconds &&
        identical(_series[chart.id], series) &&
        _activeCharts.containsKey(chart.id) &&
        definition == _definition(_activeCharts[chart.id]!);
    try {
      final data = await widget.client.data(
        chart.id,
        node: widget.node,
        afterSeconds: requestedWindow,
        points: 240,
      );
      if (!isCurrent()) return;
      series.reset(data, requestedWindow);
    } catch (_) {
      // Preserve samples and retry on the next refresh or viewport entry.
      if (isCurrent()) series.markHistoryFailed();
    } finally {
      if (identical(_historyRequests[chart.id], request)) {
        _historyRequests.remove(chart.id);
        final current = _activeCharts[chart.id];
        if (mounted &&
            source == _sourceGeneration &&
            current != null &&
            (requestedWindow != _windowSeconds ||
                definition != _definition(current))) {
          unawaited(_fetchHistory(current));
        }
      }
    }
  }

  @override
  Widget build(BuildContext context) {
    if (_loading) return const Center(child: CircularProgressIndicator());
    if (_error != null && _charts.isEmpty) {
      return _ErrorRetry(message: _error!, onRetry: _load);
    }
    final families = <String, List<Chart>>{};
    for (final chart in _visible) {
      families
          .putIfAbsent(chart.family.isEmpty ? 'other' : chart.family, () => [])
          .add(chart);
    }
    final keys = families.keys.toList()..sort();
    final source = _sourceGeneration;
    return Column(
      children: [
        Padding(
          padding: const EdgeInsets.fromLTRB(12, 8, 12, 4),
          child: Row(
            children: [
              Expanded(
                child: TextField(
                  decoration: const InputDecoration(
                    prefixIcon: Icon(Icons.search),
                    hintText: 'Filter charts',
                    isDense: true,
                    border: OutlineInputBorder(),
                  ),
                  onChanged: (value) => setState(() => _filter = value),
                ),
              ),
              const SizedBox(width: 8),
              DropdownButton<int>(
                value: _windowSeconds,
                items: const [
                  DropdownMenuItem(value: 60, child: Text('1m')),
                  DropdownMenuItem(value: 300, child: Text('5m')),
                  DropdownMenuItem(value: 900, child: Text('15m')),
                  DropdownMenuItem(value: 3600, child: Text('1h')),
                  DropdownMenuItem(value: 6 * 3600, child: Text('6h')),
                  DropdownMenuItem(value: 24 * 3600, child: Text('24h')),
                ],
                onChanged: (value) {
                  if (value == null || value == _windowSeconds) return;
                  setState(() => _windowSeconds = value);
                  _refreshActiveHistory();
                  _queueSubscriptionUpdate();
                },
              ),
            ],
          ),
        ),
        if (_error != null)
          Padding(
            padding: const EdgeInsets.symmetric(horizontal: 12),
            child: Text(
              _error!,
              style: TextStyle(color: Theme.of(context).colorScheme.error),
            ),
          ),
        Expanded(
          child: LayoutBuilder(
            builder: (context, constraints) {
              final columns = constraints.maxWidth >= 900
                  ? (constraints.maxWidth / 480).ceil()
                  : 1;
              final entries = <_ChartListEntry>[];
              for (final family in keys) {
                entries.add(_ChartListEntry(family, const []));
                final charts = families[family]!
                  ..sort((a, b) {
                    final priority = a.priority.compareTo(b.priority);
                    return priority == 0 ? a.id.compareTo(b.id) : priority;
                  });
                for (var start = 0; start < charts.length; start += columns) {
                  entries.add(
                    _ChartListEntry(
                      family,
                      charts.sublist(
                        start,
                        (start + columns).clamp(0, charts.length),
                      ),
                    ),
                  );
                }
              }
              return RefreshIndicator(
                onRefresh: _load,
                child: ListView.builder(
                  key: ValueKey(source),
                  physics: const AlwaysScrollableScrollPhysics(),
                  itemCount: entries.length,
                  itemBuilder: (context, index) {
                    final entry = entries[index];
                    if (entry.charts.isEmpty) {
                      return Padding(
                        key: ValueKey('family:${entry.family}'),
                        padding: const EdgeInsets.fromLTRB(16, 16, 16, 4),
                        child: Text(
                          entry.family.toUpperCase(),
                          style: Theme.of(
                            context,
                          ).textTheme.labelLarge?.copyWith(letterSpacing: 1.2),
                        ),
                      );
                    }
                    return Row(
                      key: ValueKey('row:${entry.charts.first.id}'),
                      crossAxisAlignment: CrossAxisAlignment.start,
                      children: [
                        for (final chart in entry.charts)
                          Expanded(
                            child: ChartCard(
                              key: ValueKey(chart.id),
                              chart: chart,
                              series: _seriesFor(chart),
                              onVisibilityChanged: (chart, active, owner) {
                                if (mounted && source == _sourceGeneration) {
                                  _setChartActive(chart, active, owner);
                                }
                              },
                            ),
                          ),
                        for (var i = entry.charts.length; i < columns; i++)
                          const Spacer(),
                      ],
                    );
                  },
                ),
              );
            },
          ),
        ),
      ],
    );
  }
}

class _ChartListEntry {
  const _ChartListEntry(this.family, this.charts);
  final String family;
  final List<Chart> charts;
}

class ChartCard extends StatefulWidget {
  const ChartCard({
    super.key,
    required this.chart,
    required this.series,
    required this.onVisibilityChanged,
  });

  final Chart chart;
  final ChartSeries series;
  final void Function(Chart, bool, Object) onVisibilityChanged;

  @override
  State<ChartCard> createState() => _ChartCardState();
}

class _ChartCardState extends State<ChartCard> {
  static const _palette = [
    Color(0xFF00A86B),
    Color(0xFF4C9BE8),
    Color(0xFFF2A93B),
    Color(0xFFE85D75),
    Color(0xFF9B6BE8),
    Color(0xFF3BC9DB),
    Color(0xFFB8C24A),
    Color(0xFFE88A4C),
  ];

  @override
  void initState() {
    super.initState();
    widget.onVisibilityChanged(widget.chart, true, this);
  }

  @override
  void didUpdateWidget(covariant ChartCard old) {
    super.didUpdateWidget(old);
    if (old.chart.id != widget.chart.id) {
      old.onVisibilityChanged(old.chart, false, this);
    }
    widget.onVisibilityChanged(widget.chart, true, this);
  }

  @override
  void dispose() {
    widget.onVisibilityChanged(widget.chart, false, this);
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final c = widget.chart;
    return Card(
      margin: const EdgeInsets.symmetric(horizontal: 8, vertical: 4),
      child: Padding(
        padding: const EdgeInsets.all(12),
        child: AnimatedBuilder(
          animation: widget.series,
          builder: (context, _) {
            final dims = c.dimensions.where((d) => !d.hidden).toList();
            final latest = <String, double>{};
            for (final d in dims) {
              final pts = widget.series.points[d.id];
              if (pts != null && pts.isNotEmpty && pts.last.isNotNull()) {
                latest[d.id] = pts.last.y;
              }
            }
            return Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Row(
                  children: [
                    Expanded(
                      child: Text(
                        c.title.isEmpty ? c.id : c.title,
                        maxLines: 1,
                        overflow: TextOverflow.ellipsis,
                        style: Theme.of(context).textTheme.titleSmall,
                      ),
                    ),
                    Text(c.units, style: Theme.of(context).textTheme.bodySmall),
                    if (widget.series.historyFailed &&
                        (widget.series.loaded || latest.isNotEmpty))
                      const Tooltip(
                        message:
                            'History refresh failed. Retrying automatically.',
                        child: Padding(
                          padding: EdgeInsets.only(left: 4),
                          child: Icon(Icons.sync_problem, size: 16),
                        ),
                      ),
                  ],
                ),
                const SizedBox(height: 6),
                SizedBox(
                  height: 120,
                  child: widget.series.loaded || latest.isNotEmpty
                      ? LineChart(_lineData(dims), duration: Duration.zero)
                      : widget.series.historyFailed
                      ? const Center(
                          child: Text(
                            'History unavailable. Retrying automatically.',
                          ),
                        )
                      : const Center(
                          child: SizedBox(
                            width: 18,
                            height: 18,
                            child: CircularProgressIndicator(strokeWidth: 2),
                          ),
                        ),
                ),
                const SizedBox(height: 6),
                Wrap(
                  spacing: 12,
                  runSpacing: 2,
                  children: [
                    for (var i = 0; i < dims.length; i++)
                      _Legend(
                        color: _palette[i % _palette.length],
                        name: dims[i].name.isEmpty ? dims[i].id : dims[i].name,
                        value: latest[dims[i].id],
                      ),
                  ],
                ),
              ],
            );
          },
        ),
      ),
    );
  }

  LineChartData _lineData(List<Dimension> dims) {
    final stacked = widget.chart.type == 'stacked';
    final bars = <LineChartBarData>[];
    for (var i = 0; i < dims.length; i++) {
      final pts = widget.series.points[dims[i].id] ?? const <FlSpot>[];
      if (!pts.any((point) => point.isNotNull())) continue;
      final isolated = <double>{};
      for (var j = 0; j < pts.length; j++) {
        if (pts[j].isNotNull() &&
            (j == 0 || pts[j - 1].isNull()) &&
            (j == pts.length - 1 || pts[j + 1].isNull())) {
          isolated.add(pts[j].x);
        }
      }
      final color = _palette[i % _palette.length];
      bars.add(
        LineChartBarData(
          spots: pts,
          color: color,
          barWidth: 1.5,
          isCurved: false,
          dotData: FlDotData(
            show: true,
            checkToShowDot: (spot, _) => isolated.contains(spot.x),
            getDotPainter: (spot, percent, bar, index) =>
                FlDotCirclePainter(radius: 2.5, color: color, strokeWidth: 0),
          ),
          belowBarData: BarAreaData(
            show: stacked || widget.chart.type == 'area',
            color: color.withValues(alpha: 0.25),
          ),
        ),
      );
    }
    final now = DateTime.now().millisecondsSinceEpoch / 1000;
    return LineChartData(
      minX: now - widget.series.window,
      maxX: now,
      lineBarsData: bars,
      lineTouchData: const LineTouchData(enabled: false),
      gridData: const FlGridData(show: true, drawVerticalLine: false),
      borderData: FlBorderData(show: false),
      titlesData: FlTitlesData(
        topTitles: const AxisTitles(),
        rightTitles: const AxisTitles(),
        bottomTitles: const AxisTitles(),
        leftTitles: AxisTitles(
          sideTitles: SideTitles(
            showTitles: true,
            reservedSize: 44,
            getTitlesWidget: (v, meta) =>
                Text(_fmt(v), style: const TextStyle(fontSize: 10)),
          ),
        ),
      ),
    );
  }
}

class _Legend extends StatelessWidget {
  const _Legend({required this.color, required this.name, this.value});

  final Color color;
  final String name;
  final double? value;

  @override
  Widget build(BuildContext context) {
    final style = Theme.of(context).textTheme.bodySmall;
    return Row(
      mainAxisSize: MainAxisSize.min,
      children: [
        Container(width: 8, height: 8, color: color),
        const SizedBox(width: 4),
        Text(name, style: style),
        const SizedBox(width: 4),
        Text(
          value == null ? '-' : _fmt(value!),
          style: style?.copyWith(fontWeight: FontWeight.bold),
        ),
      ],
    );
  }
}

String _fmt(double v) {
  final a = v.abs();
  if (a >= 1e9) return '${(v / 1e9).toStringAsFixed(1)}G';
  if (a >= 1e6) return '${(v / 1e6).toStringAsFixed(1)}M';
  if (a >= 1e3) return '${(v / 1e3).toStringAsFixed(1)}k';
  if (a >= 100) return v.toStringAsFixed(0);
  if (a >= 10) return v.toStringAsFixed(1);
  return v.toStringAsFixed(2);
}

class _ErrorRetry extends StatelessWidget {
  const _ErrorRetry({required this.message, required this.onRetry});

  final String message;
  final VoidCallback onRetry;

  @override
  Widget build(BuildContext context) {
    return Center(
      child: Column(
        mainAxisSize: MainAxisSize.min,
        children: [
          Text(
            message,
            textAlign: TextAlign.center,
            style: TextStyle(color: Theme.of(context).colorScheme.error),
          ),
          const SizedBox(height: 12),
          FilledButton.tonal(onPressed: onRetry, child: const Text('Retry')),
        ],
      ),
    );
  }
}
