import 'dart:async';

import 'package:fl_chart/fl_chart.dart';
import 'package:flutter/material.dart';

import '../api/client.dart';
import '../api/models.dart';

/// Chart list grouped by family, with history from `/api/v1/data` and live
/// updates from the WebSocket for the charts currently on screen.
class ChartsView extends StatefulWidget {
  const ChartsView({super.key, required this.client, this.node});

  final ApiClient client;
  final String? node;

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
  LiveSubscription? _live;
  StreamSubscription<LiveSample>? _liveSub;
  Timer? _reconnect;
  Timer? _refresh;

  @override
  void initState() {
    super.initState();
    _load();
    _refresh = Timer.periodic(const Duration(seconds: 30), (_) => _load());
  }

  @override
  void dispose() {
    _refresh?.cancel();
    _reconnect?.cancel();
    _liveSub?.cancel();
    _live?.close();
    super.dispose();
  }

  Future<void> _load() async {
    try {
      final list = await widget.client.charts(node: widget.node);
      if (!mounted) return;
      setState(() {
        _charts = list;
        _error = null;
        _loading = false;
      });
      _openLive();
    } catch (e) {
      if (!mounted) return;
      setState(() {
        _error = e.toString();
        _loading = false;
      });
    }
  }

  Iterable<Chart> get _visible {
    final f = _filter.trim().toLowerCase();
    return _charts.where((c) => f.isEmpty ||
        c.id.toLowerCase().contains(f) ||
        c.title.toLowerCase().contains(f) ||
        c.family.toLowerCase().contains(f));
  }

  void _openLive() {
    _reconnect?.cancel();
    _liveSub?.cancel();
    _live?.close();
    final ids = _visible.map((c) => c.id).toList();
    if (ids.isEmpty) return;
    final live = widget.client.live(charts: ids, node: widget.node);
    _live = live;
    _liveSub = live.samples.listen(
      (s) {
        final sr = _series[s.chart];
        if (sr != null) {
          sr.add(s.t, s.values);
          sr.notify();
        }
      },
      onError: (_) => _scheduleReconnect(),
      onDone: _scheduleReconnect,
    );
  }

  void _scheduleReconnect() {
    if (!mounted) return;
    _reconnect?.cancel();
    _reconnect = Timer(const Duration(seconds: 3), _openLive);
  }

  ChartSeries _seriesFor(Chart c) =>
      _series.putIfAbsent(c.id, () => ChartSeries(c, _windowSeconds));

  Future<void> _fetchHistory(Chart c) async {
    final sr = _seriesFor(c);
    if (sr.loaded && sr.window == _windowSeconds) return;
    try {
      final d = await widget.client.data(c.id,
          node: widget.node, afterSeconds: _windowSeconds, points: 240);
      sr.reset(d, _windowSeconds);
    } catch (_) {
      // keep what we have; live updates still arrive
    }
  }

  @override
  Widget build(BuildContext context) {
    if (_loading) return const Center(child: CircularProgressIndicator());
    if (_error != null && _charts.isEmpty) {
      return _ErrorRetry(message: _error!, onRetry: _load);
    }
    final families = <String, List<Chart>>{};
    for (final c in _visible) {
      families.putIfAbsent(c.family.isEmpty ? 'other' : c.family, () => []).add(c);
    }
    final keys = families.keys.toList()..sort();
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
                  onChanged: (v) => setState(() {
                    _filter = v;
                    _openLive();
                  }),
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
                onChanged: (v) {
                  if (v == null) return;
                  setState(() {
                    _windowSeconds = v;
                    for (final s in _series.values) {
                      s.loaded = false;
                    }
                  });
                },
              ),
            ],
          ),
        ),
        if (_error != null)
          Padding(
            padding: const EdgeInsets.symmetric(horizontal: 12),
            child: Text(_error!,
                style: TextStyle(color: Theme.of(context).colorScheme.error)),
          ),
        Expanded(
          child: RefreshIndicator(
            onRefresh: _load,
            child: ListView.builder(
              itemCount: keys.length,
              itemBuilder: (context, i) {
                final fam = keys[i];
                final list = families[fam]!
                  ..sort((a, b) => a.priority.compareTo(b.priority));
                return _FamilySection(
                  family: fam,
                  charts: list,
                  seriesFor: _seriesFor,
                  onVisible: _fetchHistory,
                );
              },
            ),
          ),
        ),
      ],
    );
  }
}

class _FamilySection extends StatelessWidget {
  const _FamilySection({
    required this.family,
    required this.charts,
    required this.seriesFor,
    required this.onVisible,
  });

  final String family;
  final List<Chart> charts;
  final ChartSeries Function(Chart) seriesFor;
  final Future<void> Function(Chart) onVisible;

  @override
  Widget build(BuildContext context) {
    final wide = MediaQuery.sizeOf(context).width >= 900;
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Padding(
          padding: const EdgeInsets.fromLTRB(16, 16, 16, 4),
          child: Text(family.toUpperCase(),
              style: Theme.of(context)
                  .textTheme
                  .labelLarge
                  ?.copyWith(letterSpacing: 1.2)),
        ),
        if (wide)
          GridView.builder(
            shrinkWrap: true,
            physics: const NeverScrollableScrollPhysics(),
            padding: const EdgeInsets.symmetric(horizontal: 8),
            gridDelegate: const SliverGridDelegateWithMaxCrossAxisExtent(
                maxCrossAxisExtent: 480, mainAxisExtent: 220),
            itemCount: charts.length,
            itemBuilder: (context, i) => ChartCard(
                chart: charts[i],
                series: seriesFor(charts[i]),
                onVisible: onVisible),
          )
        else
          for (final c in charts)
            ChartCard(chart: c, series: seriesFor(c), onVisible: onVisible),
      ],
    );
  }
}

/// Rolling window of points per dimension for one chart.
class ChartSeries extends ChangeNotifier {
  ChartSeries(this.chart, this.window);

  final Chart chart;
  int window;
  bool loaded = false;
  final Map<String, List<FlSpot>> points = {};

  void reset(ChartData d, int window) {
    this.window = window;
    points.clear();
    for (var i = 0; i < d.dimensionIds.length; i++) {
      final id = d.dimensionIds[i];
      final pts = <FlSpot>[];
      for (final row in d.rows) {
        final t = row.isEmpty ? null : row[0];
        final v = i + 1 < row.length ? row[i + 1] : null;
        if (t != null && v != null) pts.add(FlSpot(t, v));
      }
      points[id] = pts;
    }
    loaded = true;
    notifyListeners();
  }

  void add(int t, Map<String, double> values) {
    final cutoff = (t - window).toDouble();
    for (final e in values.entries) {
      final pts = points.putIfAbsent(e.key, () => []);
      if (pts.isNotEmpty && pts.last.x >= t) continue;
      pts.add(FlSpot(t.toDouble(), e.value));
      while (pts.isNotEmpty && pts.first.x < cutoff) {
        pts.removeAt(0);
      }
    }
  }

  void notify() => notifyListeners();
}

class ChartCard extends StatefulWidget {
  const ChartCard({
    super.key,
    required this.chart,
    required this.series,
    required this.onVisible,
  });

  final Chart chart;
  final ChartSeries series;
  final Future<void> Function(Chart) onVisible;

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
    widget.onVisible(widget.chart);
  }

  @override
  void didUpdateWidget(covariant ChartCard old) {
    super.didUpdateWidget(old);
    widget.onVisible(widget.chart);
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
              if (pts != null && pts.isNotEmpty) latest[d.id] = pts.last.y;
            }
            return Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Row(
                  children: [
                    Expanded(
                      child: Text(c.title.isEmpty ? c.id : c.title,
                          maxLines: 1,
                          overflow: TextOverflow.ellipsis,
                          style: Theme.of(context).textTheme.titleSmall),
                    ),
                    Text(c.units,
                        style: Theme.of(context).textTheme.bodySmall),
                  ],
                ),
                const SizedBox(height: 6),
                SizedBox(
                  height: 120,
                  child: widget.series.loaded || latest.isNotEmpty
                      ? LineChart(_lineData(dims))
                      : const Center(
                          child: SizedBox(
                              width: 18,
                              height: 18,
                              child: CircularProgressIndicator(strokeWidth: 2))),
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
      if (pts.length < 2) continue;
      final color = _palette[i % _palette.length];
      bars.add(LineChartBarData(
        spots: pts,
        color: color,
        barWidth: 1.5,
        isCurved: false,
        dotData: const FlDotData(show: false),
        belowBarData: BarAreaData(
            show: stacked || widget.chart.type == 'area',
            color: color.withValues(alpha: 0.25)),
      ));
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
            getTitlesWidget: (v, meta) => Text(_fmt(v),
                style: const TextStyle(fontSize: 10)),
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
        Text(value == null ? '-' : _fmt(value!),
            style: style?.copyWith(fontWeight: FontWeight.bold)),
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
          Text(message,
              textAlign: TextAlign.center,
              style: TextStyle(color: Theme.of(context).colorScheme.error)),
          const SizedBox(height: 12),
          FilledButton.tonal(onPressed: onRetry, child: const Text('Retry')),
        ],
      ),
    );
  }
}
