import 'dart:async';

import 'package:flutter/material.dart';

import '../api/client.dart';

/// Problems from the operations overview, with acknowledgement for operators.
class OperationsView extends StatefulWidget {
  const OperationsView({super.key, required this.client, this.active = true, this.canHandle = false});

  final ApiClient client;
  final bool active;
  final bool canHandle;

  @override
  State<OperationsView> createState() => _OperationsViewState();
}

class _OperationsViewState extends State<OperationsView> {
  List<Map<String, dynamic>> _problems = const [];
  String? _error;
  bool _loading = true;
  Timer? _timer;
  int _generation = 0;

  @override
  void initState() {
    super.initState();
    if (widget.active) _load();
  }

  @override
  void didUpdateWidget(covariant OperationsView oldWidget) {
    super.didUpdateWidget(oldWidget);
    if (oldWidget.client != widget.client || oldWidget.active != widget.active) {
      _timer?.cancel();
      ++_generation;
      if (widget.active) _load();
    }
  }

  @override
  void dispose() {
    _timer?.cancel();
    ++_generation;
    super.dispose();
  }

  Future<void> _load() async {
    final generation = ++_generation;
    _timer?.cancel();
    try {
      final body = await widget.client.operations();
      if (!mounted || generation != _generation) return;
      final list = (body['problems'] as List<dynamic>?) ?? const [];
      setState(() {
        _problems = list.map((e) => Map<String, dynamic>.from(e as Map)).toList();
        _error = null;
        _loading = false;
      });
    } catch (e) {
      if (!mounted || generation != _generation) return;
      setState(() {
        _error = '$e';
        _loading = false;
      });
    } finally {
      if (mounted && generation == _generation && widget.active) {
        _timer = Timer(const Duration(seconds: 15), _load);
      }
    }
  }

  Future<void> _ack(Map<String, dynamic> problem) async {
    final handling = problem['handling'] as Map<String, dynamic>? ?? const {};
    try {
      await widget.client.acknowledgeProblem(problem['id'] as String, (handling['revision'] as num?)?.toInt() ?? 0);
      await _load();
    } catch (e) {
      if (!mounted) return;
      setState(() => _error = '$e');
    }
  }

  @override
  Widget build(BuildContext context) {
    if (_loading && _problems.isEmpty) return const Center(child: CircularProgressIndicator());
    return ListView(
      padding: const EdgeInsets.all(12),
      children: [
        if (_error != null) Text(_error!, style: TextStyle(color: Theme.of(context).colorScheme.error)),
        if (_problems.isEmpty) const Text('没有待展示的问题。'),
        for (final p in _problems)
          Card(
            child: ListTile(
              title: Text('${p['hostname'] ?? ''} · ${p['name'] ?? ''}'),
              subtitle: Text('${p['severity'] ?? ''} · ${p['chart'] ?? ''}'),
              trailing: widget.canHandle && ((p['handling'] as Map?)?['acknowledged'] != true)
                  ? TextButton(onPressed: () => _ack(p), child: const Text('确认'))
                  : null,
            ),
          ),
      ],
    );
  }
}
