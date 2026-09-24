import 'package:flutter/material.dart';

import '../api/models.dart';
import '../state/app_state.dart';
import '../widgets/agent_config_view.dart';
import '../widgets/alarms_view.dart';
import '../widgets/charts_view.dart';
import '../widgets/functions_view.dart';
import '../widgets/operations_view.dart';

class HomeScreen extends StatefulWidget {
  const HomeScreen({super.key, required this.state});

  final AppState state;

  @override
  State<HomeScreen> createState() => _HomeScreenState();
}

class _HomeScreenState extends State<HomeScreen> with WidgetsBindingObserver {
  int _tab = 0;
  late bool _visible;

  bool _isVisible(AppLifecycleState? state) =>
      state == null ||
      state == AppLifecycleState.resumed ||
      state == AppLifecycleState.inactive;

  @override
  void initState() {
    super.initState();
    _visible = _isVisible(WidgetsBinding.instance.lifecycleState);
    WidgetsBinding.instance.addObserver(this);
  }

  @override
  void didChangeAppLifecycleState(AppLifecycleState state) {
    final visible = _isVisible(state);
    if (_visible != visible) setState(() => _visible = visible);
  }

  @override
  void dispose() {
    WidgetsBinding.instance.removeObserver(this);
    super.dispose();
  }

  String? get _nodeParam {
    final st = widget.state;
    if (st.info?.isHub != true) return null;
    return st.selectedNode;
  }

  @override
  Widget build(BuildContext context) {
    final st = widget.state;
    final info = st.info!;
    final node = st.currentNode;
    final title = node?.hostname ?? info.hostname;
    final showConfig = info.role == 'admin' && (st.selectedNode == 'local' || st.selectedNode.isEmpty);
    final tab = showConfig || _tab < 4 ? _tab : 3;
    final pages = [
      ChartsView(
          key: ValueKey('charts:${st.selectedNode}'),
          client: st.client!,
          node: _nodeParam,
          active: _visible && _tab == 0),
      AlarmsView(
          key: ValueKey('alarms:${st.selectedNode}'),
          client: st.client!,
          node: _nodeParam,
          active: _visible && _tab == 1),
      FunctionsView(
          key: ValueKey('functions:${st.selectedNode}'),
          client: st.client!,
          node: _nodeParam,
          active: _visible && _tab == 2),
      OperationsView(
          key: ValueKey('ops:${st.selectedNode}'),
          client: st.client!,
          canHandle: info.role == 'admin' || info.role == 'troubleshooter',
          active: _visible && tab == 3),
      if (showConfig)
        AgentConfigView(key: const ValueKey('agent-config'), client: st.client!),
    ];
    return Scaffold(
      appBar: AppBar(
        title: Text(title),
        actions: [
          if (info.isHub) _NodePicker(state: st),
          IconButton(
            tooltip: 'Disconnect',
            icon: const Icon(Icons.logout),
            onPressed: st.disconnect,
          ),
        ],
      ),
      body: Column(
        children: [
          for (final message in [st.storageWarning, st.error].whereType<String>())
            Padding(
              padding: const EdgeInsets.symmetric(horizontal: 16, vertical: 8),
              child: Text(
                message,
                maxLines: 3,
                overflow: TextOverflow.ellipsis,
                style: TextStyle(color: Theme.of(context).colorScheme.error),
              ),
            ),
          Expanded(child: IndexedStack(index: tab, children: pages)),
        ],
      ),
      bottomNavigationBar: NavigationBar(
        selectedIndex: tab,
        onDestinationSelected: (i) => setState(() => _tab = i),
        destinations: [
          const NavigationDestination(
              icon: Icon(Icons.show_chart), label: 'Charts'),
          NavigationDestination(
            icon: _AlarmIcon(counts: node?.alarms ?? info.alarms),
            label: 'Alarms',
          ),
          const NavigationDestination(
              icon: Icon(Icons.table_rows), label: 'Functions'),
          const NavigationDestination(
              icon: Icon(Icons.assignment_outlined), label: 'Ops'),
          if (showConfig)
            const NavigationDestination(icon: Icon(Icons.tune), label: 'Config'),
        ],
      ),
    );
  }
}

class _AlarmIcon extends StatelessWidget {
  const _AlarmIcon({required this.counts});

  final AlarmCounts counts;

  @override
  Widget build(BuildContext context) {
    final n = counts.warning + counts.critical;
    if (n == 0) return const Icon(Icons.notifications_none);
    return Badge(
      label: Text('$n'),
      backgroundColor: counts.critical > 0 ? Colors.red : Colors.orange,
      child: const Icon(Icons.notifications_active),
    );
  }
}

class _NodePicker extends StatelessWidget {
  const _NodePicker({required this.state});

  final AppState state;

  @override
  Widget build(BuildContext context) {
    return PopupMenuButton<String>(
      tooltip: 'Select node',
      icon: const Icon(Icons.dns),
      onOpened: state.refreshNodes,
      onSelected: state.selectNode,
      itemBuilder: (context) => [
        for (final n in state.nodes)
          PopupMenuItem(
            value: n.selector,
            child: Row(
              children: [
                Icon(Icons.circle,
                    size: 10,
                    color: switch (n.status) {
                      'live' => Colors.green,
                      'stale' => Colors.orange,
                      _ => Colors.grey,
                    }),
                const SizedBox(width: 8),
                Expanded(
                  child: Text(n.hostname,
                      style: TextStyle(
                          fontWeight: n.selector == state.selectedNode
                              ? FontWeight.bold
                              : FontWeight.normal)),
                ),
                const SizedBox(width: 8),
                Text('${n.os}/${n.arch}',
                    style: Theme.of(context).textTheme.bodySmall),
              ],
            ),
          ),
      ],
    );
  }
}
