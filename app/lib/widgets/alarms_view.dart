import 'dart:async';

import 'package:flutter/material.dart';

import '../api/client.dart';
import '../api/models.dart';

/// Active alarms (non-CLEAR first) plus the recent transition log.
class AlarmsView extends StatefulWidget {
  const AlarmsView({
    super.key,
    required this.client,
    this.node,
    this.active = true,
  });

  final ApiClient client;
  final String? node;
  final bool active;

  @override
  State<AlarmsView> createState() => _AlarmsViewState();
}

class _AlarmsViewState extends State<AlarmsView> {
  List<Alarm> _alarms = const [];
  List<AlarmLogEntry> _log = const [];
  String? _error;
  bool _loading = true;
  bool _showAll = false;
  Timer? _timer;
  Future<void>? _pending;
  int _generation = 0;
  bool _hasSnapshot = false;

  @override
  void initState() {
    super.initState();
    if (widget.active) _load();
  }

  void _stop() {
    ++_generation;
    _timer?.cancel();
    _pending = null;
  }

  @override
  void didUpdateWidget(covariant AlarmsView oldWidget) {
    super.didUpdateWidget(oldWidget);
    final sourceChanged =
        oldWidget.client != widget.client || oldWidget.node != widget.node;
    if (!sourceChanged && oldWidget.active == widget.active) return;
    _stop();
    if (sourceChanged) {
      _alarms = const [];
      _log = const [];
      _error = null;
      _loading = true;
      _hasSnapshot = false;
    }
    if (widget.active) _load();
  }

  @override
  void dispose() {
    _stop();
    super.dispose();
  }

  Future<void> _load() {
    if (!mounted || !widget.active) return Future.value();
    if (_pending != null) return _pending!;
    _timer?.cancel();
    final generation = ++_generation;
    return _pending = _fetch(generation).whenComplete(() {
      if (!mounted || generation != _generation || !widget.active) return;
      _pending = null;
      // Schedule after completion, so slow servers never accumulate requests.
      _timer = Timer(const Duration(seconds: 10), _load);
    });
  }

  Future<void> _fetch(int generation) async {
    try {
      final results = await Future.wait([
        widget.client.alarms(node: widget.node, all: true),
        widget.client.alarmLog(node: widget.node),
      ]);
      if (!mounted || generation != _generation) return;
      setState(() {
        _alarms = results[0] as List<Alarm>;
        _log = results[1] as List<AlarmLogEntry>;
        _error = null;
        _loading = false;
        _hasSnapshot = true;
      });
    } catch (e) {
      if (!mounted || generation != _generation) return;
      setState(() {
        _error = e.toString();
        _loading = false;
      });
    }
  }

  @override
  Widget build(BuildContext context) {
    if (_loading) return const Center(child: CircularProgressIndicator());
    if (!_hasSnapshot) {
      return Center(
        child: Padding(
          padding: const EdgeInsets.all(16),
          child: Column(
            mainAxisSize: MainAxisSize.min,
            children: [
              const Text('Alarm status unavailable'),
              if (_error != null) Text(_error!, textAlign: TextAlign.center),
              const SizedBox(height: 12),
              FilledButton.tonal(onPressed: _load, child: const Text('Retry')),
            ],
          ),
        ),
      );
    }
    final raised = _alarms
        .where((a) => a.status == 'WARNING' || a.status == 'CRITICAL')
        .toList();
    final shown = _showAll ? _alarms : raised;
    return RefreshIndicator(
      onRefresh: _load,
      child: ListView(
        physics: const AlwaysScrollableScrollPhysics(),
        children: [
          if (_error != null)
            Padding(
              padding: const EdgeInsets.all(12),
              child: Text(
                'Refresh failed; showing the last successful snapshot.\n$_error',
                style: TextStyle(color: Theme.of(context).colorScheme.error),
              ),
            ),
          ListTile(
            title: Text(
              'Alarms (${raised.length} raised / ${_alarms.length} total)',
            ),
            trailing: TextButton(
              onPressed: () => setState(() => _showAll = !_showAll),
              child: Text(_showAll ? 'Raised only' : 'Show all'),
            ),
          ),
          if (shown.isEmpty)
            Padding(
              padding: const EdgeInsets.all(24),
              child: Center(
                child: Text(_showAll ? 'No alarms' : 'No raised alarms'),
              ),
            ),
          for (final a in shown) _AlarmTile(alarm: a),
          const Divider(),
          const ListTile(title: Text('Recent transitions')),
          if (_log.isEmpty)
            const Padding(
              padding: EdgeInsets.all(24),
              child: Center(child: Text('No transitions yet')),
            ),
          for (final e in _log.take(100))
            ListTile(
              dense: true,
              leading: _StatusDot(status: e.status),
              title: Text('${e.name} · ${e.chart}'),
              subtitle: Text(
                '${e.oldStatus} → ${e.status}  ${_fmtValue(e.value, e.units)}',
              ),
              trailing: Text(
                _ago(e.when),
                style: Theme.of(context).textTheme.bodySmall,
              ),
            ),
        ],
      ),
    );
  }
}

class _AlarmTile extends StatelessWidget {
  const _AlarmTile({required this.alarm});

  final Alarm alarm;

  @override
  Widget build(BuildContext context) {
    return ListTile(
      leading: _StatusDot(status: alarm.status),
      title: Text(alarm.name),
      subtitle: Text(
        [alarm.chart, if (alarm.info.isNotEmpty) alarm.info].join(' — '),
        maxLines: 2,
        overflow: TextOverflow.ellipsis,
      ),
      trailing: Column(
        mainAxisAlignment: MainAxisAlignment.center,
        crossAxisAlignment: CrossAxisAlignment.end,
        children: [
          Text(
            _fmtValue(alarm.value, alarm.units),
            style: const TextStyle(fontWeight: FontWeight.bold),
          ),
          Text(
            _ago(alarm.lastStatusChange),
            style: Theme.of(context).textTheme.bodySmall,
          ),
        ],
      ),
    );
  }
}

class _StatusDot extends StatelessWidget {
  const _StatusDot({required this.status});

  final String status;

  @override
  Widget build(BuildContext context) {
    final color = switch (status) {
      'CRITICAL' => Colors.red,
      'WARNING' => Colors.orange,
      'CLEAR' => Colors.green,
      _ => Colors.grey,
    };
    return Tooltip(
      message: status,
      child: Icon(Icons.circle, color: color, size: 14),
    );
  }
}

String _fmtValue(double v, String units) {
  if (v.isNaN) return '-';
  final s = v.abs() >= 100 ? v.toStringAsFixed(0) : v.toStringAsFixed(2);
  return units.isEmpty ? s : '$s $units';
}

String _ago(int unix) {
  if (unix <= 0) return '';
  final d = DateTime.now().difference(
    DateTime.fromMillisecondsSinceEpoch(unix * 1000),
  );
  if (d.inSeconds < 60) return '${d.inSeconds}s ago';
  if (d.inMinutes < 60) return '${d.inMinutes}m ago';
  if (d.inHours < 24) return '${d.inHours}h ago';
  return '${d.inDays}d ago';
}
