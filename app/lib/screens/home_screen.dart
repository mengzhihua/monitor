import 'package:flutter/material.dart';

import '../api/models.dart';
import '../state/app_state.dart';
import '../widgets/alarms_view.dart';
import '../widgets/charts_view.dart';
import '../widgets/functions_view.dart';

class HomeScreen extends StatefulWidget {
  const HomeScreen({super.key, required this.state});

  final AppState state;

  @override
  State<HomeScreen> createState() => _HomeScreenState();
}

class _HomeScreenState extends State<HomeScreen> {
  int _tab = 0;

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
    final pages = [
      ChartsView(
          key: ValueKey('charts:${st.selectedNode}'),
          client: st.client!,
          node: _nodeParam,
          active: _tab == 0),
      AlarmsView(
          key: ValueKey('alarms:${st.selectedNode}'),
          client: st.client!,
          node: _nodeParam,
          active: _tab == 1),
      FunctionsView(
          key: ValueKey('functions:${st.selectedNode}'),
          client: st.client!,
          node: _nodeParam,
          active: _tab == 2),
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
      body: IndexedStack(index: _tab, children: pages),
      bottomNavigationBar: NavigationBar(
        selectedIndex: _tab,
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
