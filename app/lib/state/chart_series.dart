import 'package:fl_chart/fl_chart.dart';
import 'package:flutter/foundation.dart';

import '../api/models.dart';

/// Aligned, bounded samples. Missing values stay as gaps in every dimension.
class ChartSeries extends ChangeNotifier {
  ChartSeries(this.chart, this.window);

  Chart chart;
  int window;
  bool loaded = false;
  bool historyFailed = false;
  final Map<String, List<FlSpot>> points = {};
  final _times = <double>[];
  final _liveTail = <({int t, Map<String, double> values})>[];

  void reset(ChartData data, int window) {
    // Only live samples beyond the response's time range may survive a reload.
    // Old aggregated history must never be replayed as second-resolution data.
    final tail = loaded && this.window == window && window <= 1200
        ? _liveTail.toList()
        : <({int t, Map<String, double> values})>[];
    this.window = window;
    _liveTail.clear();
    _times.clear();
    points.clear();
    final columns = {
      for (var i = 0; i < data.dimensionIds.length; i++)
        data.dimensionIds[i]: i + 1,
    };
    for (final id in {...columns.keys, ...chart.dimensions.map((d) => d.id)}) {
      points[id] = [];
    }
    for (final row in data.rows) {
      final time = row.isEmpty ? null : row[0];
      if (time == null ||
          !time.isFinite ||
          (_times.isNotEmpty && time <= _times.last)) {
        continue;
      }
      _times.add(time);
      for (final entry in points.entries) {
        final column = columns[entry.key];
        final value = column != null && column < row.length
            ? row[column]
            : null;
        entry.value.add(_spot(time, value));
      }
    }
    final historyEnd = _times.isEmpty ? double.negativeInfinity : _times.last;
    for (final sample in tail) {
      if (sample.t > historyEnd) add(sample.t, sample.values);
    }
    _trim();
    loaded = true;
    historyFailed = false;
    notifyListeners();
  }

  bool add(int time, Map<String, double> values) {
    if (window > 1200 || (_times.isNotEmpty && time <= _times.last)) {
      return false;
    }
    for (final id in {...values.keys, ...chart.dimensions.map((d) => d.id)}) {
      points.putIfAbsent(
        id,
        () => List.filled(_times.length, FlSpot.nullSpot, growable: true),
      );
    }
    final cadence = chart.updateEvery > 0 ? chart.updateEvery : 1;
    if (_times.isNotEmpty && time - _times.last >= 2 * cadence) {
      _append((time - cadence).toDouble(), const {});
    }
    _append(time.toDouble(), values);
    _liveTail.add((t: time, values: Map.of(values)));
    _trim();
    return true;
  }

  void _append(double time, Map<String, double> values) {
    _times.add(time);
    for (final entry in points.entries) {
      entry.value.add(_spot(time, values[entry.key]));
    }
  }

  FlSpot _spot(double time, double? value) =>
      value != null && value.isFinite ? FlSpot(time, value) : FlSpot.nullSpot;

  void _trim() {
    if (_times.isEmpty) return;
    final cutoff = _times.last - window;
    var remove = 0;
    while (remove < _times.length &&
        (_times[remove] < cutoff || _times.length - remove > 1200)) {
      remove++;
    }
    if (remove > 0) {
      _times.removeRange(0, remove);
      for (final values in points.values) {
        values.removeRange(0, remove);
      }
    }
    var removeLive = 0;
    while (removeLive < _liveTail.length &&
        (_liveTail[removeLive].t < cutoff ||
            _liveTail.length - removeLive > 1200)) {
      removeLive++;
    }
    if (removeLive > 0) _liveTail.removeRange(0, removeLive);
  }

  void markHistoryFailed() {
    historyFailed = true;
    notifyListeners();
  }

  void notify() => notifyListeners();
}
