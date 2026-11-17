import 'package:flutter/material.dart';

import '../state/app_state.dart';

class NodePicker extends StatelessWidget {
  const NodePicker({super.key, required this.state});

  final AppState state;

  @override
  Widget build(BuildContext context) => IconButton(
    tooltip: 'Select node',
    icon: const Icon(Icons.dns),
    onPressed: () async {
      final selector = await showDialog<String>(
        context: context,
        builder: (_) => _NodeDialog(state: state),
      );
      if (context.mounted && selector != null) await state.selectNode(selector);
    },
  );
}

class _NodeDialog extends StatefulWidget {
  const _NodeDialog({required this.state});
  final AppState state;

  @override
  State<_NodeDialog> createState() => _NodeDialogState();
}

class _NodeDialogState extends State<_NodeDialog> {
  String query = '';
  late final refresh = widget.state.refreshNodes();

  @override
  Widget build(BuildContext context) {
    final state = widget.state;
    return Dialog(
      insetPadding: const EdgeInsets.symmetric(horizontal: 16, vertical: 12),
      child: ConstrainedBox(
        constraints: const BoxConstraints(maxWidth: 440, maxHeight: 480),
        child: Column(
          children: [
            ListTile(
              title: const Text('Select node'),
              trailing: IconButton(
                tooltip: 'Close',
                icon: const Icon(Icons.close),
                onPressed: () => Navigator.pop(context),
              ),
            ),
            Padding(
              padding: const EdgeInsets.symmetric(horizontal: 16),
              child: TextField(
                decoration: const InputDecoration(
                  labelText: 'Search hostname or node ID',
                  prefixIcon: Icon(Icons.search),
                ),
                onChanged: (value) =>
                    setState(() => query = value.trim().toLowerCase()),
              ),
            ),
            Expanded(
              child: FutureBuilder<void>(
                future: refresh,
                builder: (context, snapshot) => ListenableBuilder(
                  listenable: state,
                  builder: (context, _) {
                    if (state.nodes.isEmpty &&
                        snapshot.connectionState == ConnectionState.waiting) {
                      return const Center(child: CircularProgressIndicator());
                    }
                    final nodes = state.nodes
                        .where(
                          (n) =>
                              n.hostname.toLowerCase().contains(query) ||
                              n.id.toLowerCase().contains(query),
                        )
                        .toList();
                    if (nodes.isEmpty) {
                      return Center(
                        child: Text(
                          state.nodes.isEmpty
                              ? state.error ?? 'No nodes available.'
                              : 'No nodes match your search.',
                        ),
                      );
                    }
                    return ListView.builder(
                      itemCount: nodes.length,
                      itemBuilder: (context, index) {
                        final n = nodes[index];
                        final selected = n.selector == state.selectedNode;
                        return ListTile(
                          selected: selected,
                          title: Text(
                            n.hostname,
                            maxLines: 2,
                            overflow: TextOverflow.ellipsis,
                          ),
                          subtitle: Text(
                            [
                              if (!n.local) n.id,
                              n.status,
                              [
                                n.os,
                                n.arch,
                              ].where((v) => v.isNotEmpty).join('/'),
                            ].where((v) => v.isNotEmpty).join(' · '),
                          ),
                          trailing: selected ? const Icon(Icons.check) : null,
                          onTap: () => Navigator.pop(context, n.selector),
                        );
                      },
                    );
                  },
                ),
              ),
            ),
          ],
        ),
      ),
    );
  }
}
