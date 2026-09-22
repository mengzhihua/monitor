import 'dart:async';
import 'dart:convert';

import 'package:flutter/material.dart';

import '../api/client.dart';
import '../api/models.dart';

/// Live Functions tables (`/api/v1/functions` + `/function`), matching the
/// embedded Vue panel: pick a function, filter rows, tap numeric columns to sort.
class FunctionsView extends StatefulWidget {
  const FunctionsView({super.key, required this.client, this.node});

  final ApiClient client;
  final String? node;

  @override
  State<FunctionsView> createState() => _FunctionsViewState();
}

class _FunctionsViewState extends State<FunctionsView> {
  List<FunctionInfo> _fns = const [];
  String _selected = '';
  String _sort = 'cpu';
  String _filter = '';
  FunctionResult? _result;
  String? _error;
  bool _loading = true;
  Timer? _timer;

  @override
  void initState() {
    super.initState();
    _bootstrap();
    _timer = Timer.periodic(const Duration(seconds: 2), (_) => _load());
  }

  @override
  void dispose() {
    _timer?.cancel();
    super.dispose();
  }

  Future<void> _bootstrap() async {
    try {
      final list = await widget.client.functions(node: widget.node);
      if (!mounted) return;
      setState(() {
        _fns = list;
        if (_selected.isEmpty && list.isNotEmpty) {
          _selected = list.first.name;
        }
        _error = null;
      });
      await _load();
    } catch (e) {
      if (!mounted) return;
      setState(() {
        _error = e.toString();
        _loading = false;
      });
    }
  }

  Future<void> _load() async {
    if (_selected.isEmpty) {
      if (mounted) setState(() => _loading = false);
      return;
    }
    try {
      final args = <String, String>{};
      if (_selected == 'processes' ||
          _selected == 'services' ||
          _selected == 'network-connections' ||
          _selected == 'windows-services') {
        if (_sort.isNotEmpty) args['sort'] = _sort;
      }
      if (_filter.trim().isNotEmpty &&
          (_selected == 'logs' || _selected == 'windows-services')) {
        args['query'] = _filter.trim();
      }
      final r = await widget.client.function(_selected, node: widget.node, args: args);
      if (!mounted) return;
      setState(() {
        _result = r;
        _error = null;
        _loading = false;
      });
    } catch (e) {
      if (!mounted) return;
      setState(() {
        _error = e.toString();
        _loading = false;
      });
    }
  }

  List<Map<String, dynamic>> _rows() {
    final table = _result?.table;
    if (table == null) return const [];
    var list = table.rows;
    final q = _filter.trim().toLowerCase();
    if (q.isNotEmpty) {
      list = list
          .where((r) => r.values.any((v) => v.toString().toLowerCase().contains(q)))
          .toList();
    }
    if (_sort.isNotEmpty && list.isNotEmpty && list.first[_sort] is num) {
      list = [...list]..sort((a, b) => _num(b[_sort]).compareTo(_num(a[_sort])));
    }
    return list;
  }

  double _num(Object? v) => v is num ? v.toDouble() : 0;

  bool _sortable(String col) {
    final rows = _result?.table?.rows;
    if (rows == null || rows.isEmpty) return false;
    return rows.first[col] is num;
  }

  String _fmt(String col, Object? v) {
    if (v == null) return '';
    if (v is! num) return v.toString();
    if (col == 'rss' || col == 'memory') {
      final n = v.toDouble();
      if (n >= 1 << 30) return '${(n / (1 << 30)).toStringAsFixed(2)} GiB';
      if (n >= 1 << 20) return '${(n / (1 << 20)).toStringAsFixed(1)} MiB';
      return n.toStringAsFixed(0);
    }
    if (col == 'cpu') return '${v.toStringAsFixed(1)}%';
    if (col == 'rtt' || col == 'latency') {
      return '${v.toStringAsFixed(1)} ms';
    }
    if (v is int || v == v.roundToDouble()) return v.toStringAsFixed(0);
    return v.toStringAsFixed(2);
  }

  @override
  Widget build(BuildContext context) {
    if (_loading && _fns.isEmpty) {
      return const Center(child: CircularProgressIndicator());
    }
    if (_fns.isEmpty) {
      return Center(child: Text(_error ?? 'No functions on this node'));
    }
    final table = _result?.table;
    final rows = _rows();
    return Column(
      children: [
        Padding(
          padding: const EdgeInsets.fromLTRB(12, 8, 12, 4),
          child: Row(
            children: [
              Expanded(
                child: DropdownButton<String>(
                  isExpanded: true,
                  value: _fns.any((f) => f.name == _selected) ? _selected : _fns.first.name,
                  items: [
                    for (final f in _fns)
                      DropdownMenuItem(value: f.name, child: Text(f.help.isEmpty ? f.name : '${f.name} — ${f.help}')),
                  ],
                  onChanged: (v) {
                    if (v == null) return;
                    setState(() => _selected = v);
                    _load();
                  },
                ),
              ),
              const SizedBox(width: 8),
              if (table != null)
                Text('${rows.length} / ${table.total}',
                    style: Theme.of(context).textTheme.bodySmall),
            ],
          ),
        ),
        Padding(
          padding: const EdgeInsets.symmetric(horizontal: 12),
          child: TextField(
            decoration: const InputDecoration(
              hintText: 'Filter…',
              isDense: true,
              prefixIcon: Icon(Icons.search, size: 18),
            ),
            onChanged: (v) => setState(() => _filter = v),
          ),
        ),
        if (_error != null)
          Padding(
            padding: const EdgeInsets.all(12),
            child: Text(_error!, style: TextStyle(color: Theme.of(context).colorScheme.error)),
          ),
        Expanded(
          child: table == null
              ? SingleChildScrollView(
                  padding: const EdgeInsets.all(12),
                  child: SelectableText(
                    _result?.raw == null ? '' : const JsonEncoder.withIndent('  ').convert(_result!.raw),
                  ),
                )
              : RefreshIndicator(
                  onRefresh: _load,
                  child: ListView(
                    children: [
                      SingleChildScrollView(
                        scrollDirection: Axis.horizontal,
                        child: DataTable(
                          headingRowHeight: 36,
                          dataRowMinHeight: 32,
                          dataRowMaxHeight: 48,
                          columns: [
                            for (final c in table.columns)
                              DataColumn(
                                label: Text(c,
                                    style: TextStyle(
                                        fontWeight: _sort == c ? FontWeight.bold : FontWeight.normal)),
                                onSort: _sortable(c)
                                    ? (_, __) => setState(() => _sort = c)
                                    : null,
                                numeric: _sortable(c),
                              ),
                          ],
                          rows: [
                            for (final r in rows)
                              DataRow(cells: [
                                for (final c in table.columns)
                                  DataCell(SizedBox(
                                    width: c == 'cmdline' || c == 'message' ? 280 : null,
                                    child: Text(_fmt(c, r[c]),
                                        maxLines: 2, overflow: TextOverflow.ellipsis),
                                  )),
                              ]),
                          ],
                        ),
                      ),
                      if (rows.isEmpty)
                        const Padding(
                          padding: EdgeInsets.all(24),
                          child: Center(child: Text('No rows')),
                        ),
                    ],
                  ),
                ),
        ),
      ],
    );
  }
}
