import 'package:flutter/material.dart';

import '../api/client.dart';

class AgentConfigView extends StatefulWidget {
  const AgentConfigView({super.key, required this.client});

  final ApiClient client;

  @override
  State<AgentConfigView> createState() => _AgentConfigViewState();
}

class _AgentConfigViewState extends State<AgentConfigView> {
  final _yaml = TextEditingController();
  String _path = '';
  String _notice = '';
  String _error = '';
  bool _writable = false;
  bool _busy = false;

  @override
  void initState() {
    super.initState();
    _load();
  }

  @override
  void dispose() {
    _yaml.dispose();
    super.dispose();
  }

  Future<void> _load() async {
    setState(() {
      _error = '';
      _busy = true;
    });
    try {
      final cfg = await widget.client.agentConfig();
      if (!mounted) return;
      _path = (cfg['path'] as String?) ?? '';
      _yaml.text = (cfg['yaml'] as String?) ?? '';
      _writable = cfg['writable'] == true;
    } on ApiException catch (e) {
      if (!mounted) return;
      _error = e.message;
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }

  Future<void> _save() async {
    if (!_writable || _busy) return;
    setState(() {
      _busy = true;
      _error = '';
      _notice = '';
    });
    try {
      await widget.client.saveAgentConfig(_yaml.text);
      if (!mounted) return;
      _notice = '已写入配置文件。重启本机服务后才会生效。';
    } on ApiException catch (e) {
      if (!mounted) return;
      _error = e.message;
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }

  Future<void> _restart() async {
    if (_busy) return;
    final ok = await showDialog<bool>(
      context: context,
      builder: (context) => AlertDialog(
        title: const Text('重启服务'),
        content: const Text('重启会中断当前连接，并让已保存的配置生效。'),
        actions: [
          TextButton(onPressed: () => Navigator.pop(context, false), child: const Text('取消')),
          TextButton(onPressed: () => Navigator.pop(context, true), child: const Text('重启')),
        ],
      ),
    );
    if (ok != true || !mounted) return;
    setState(() {
      _busy = true;
      _error = '';
      _notice = '';
    });
    try {
      await widget.client.restartAgent();
      if (!mounted) return;
      _notice = '服务正在重启。请稍等几秒后重新连接。';
    } on ApiException catch (e) {
      if (!mounted) return;
      _error = e.message;
      setState(() => _busy = false);
    }
  }

  @override
  Widget build(BuildContext context) {
    return ListView(
      padding: const EdgeInsets.all(16),
      children: [
        Text(_path.isEmpty ? '未绑定配置文件' : _path),
        const SizedBox(height: 8),
        const Text('这里修改的是当前连接的这台 monitord。查看远端节点时不提供此页。'),
        if (_error.isNotEmpty) ...[
          const SizedBox(height: 8),
          Text(_error, style: TextStyle(color: Theme.of(context).colorScheme.error)),
        ],
        if (_notice.isNotEmpty) ...[
          const SizedBox(height: 8),
          Text(_notice),
        ],
        const SizedBox(height: 8),
        TextField(
          controller: _yaml,
          readOnly: !_writable,
          maxLines: 16,
          decoration: const InputDecoration(labelText: '本机配置 YAML', border: OutlineInputBorder()),
        ),
        const SizedBox(height: 8),
        Row(
          children: [
            FilledButton(onPressed: _writable && !_busy ? _save : null, child: const Text('保存配置')),
            const SizedBox(width: 8),
            OutlinedButton(onPressed: _busy ? null : _restart, child: const Text('重启服务')),
          ],
        ),
      ],
    );
  }
}
