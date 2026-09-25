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
  final _host = TextEditingController();
  final _listen = TextEditingController();
  final _nginx = TextEditingController();
  final _slack = TextEditingController();
  Map<String, dynamic>? _form;
  int? _updated;
  String _path = '';
  String _backup = '';
  String _notice = '';
  String _error = '';
  bool _writable = false;
  bool _busy = false;
  bool _silent = false;

  @override
  void initState() {
    super.initState();
    _load();
  }

  @override
  void dispose() {
    _yaml.dispose();
    _host.dispose();
    _listen.dispose();
    _nginx.dispose();
    _slack.dispose();
    super.dispose();
  }

  void _apply(Map<String, dynamic> cfg) {
    _path = (cfg['path'] as String?) ?? '';
    _yaml.text = (cfg['yaml'] as String?) ?? '';
    _writable = cfg['writable'] == true;
    _updated = cfg['updated'] is int ? cfg['updated'] as int : null;
    _backup = (cfg['backup'] as String?) ?? '';
    final form = cfg['form'];
    if (form is Map) {
      _form = Map<String, dynamic>.from(form);
      _host.text = (_form!['hostname'] as String?) ?? '';
      _listen.text = (_form!['listen'] as String?) ?? '';
      _silent = _form!['health_silent'] == true;
      final notify = _form!['notify'];
      if (notify is Map) {
        _slack.text = (notify['slack_webhook_url'] as String?) ?? '';
      }
      final targets = _form!['targets'];
      if (targets is List) {
        for (final item in targets) {
          if (item is Map && item['name'] == 'nginx') {
            _nginx.text = (item['url'] as String?) ?? '';
          }
        }
      }
    }
  }

  Future<void> _load() async {
    setState(() {
      _error = '';
      _busy = true;
    });
    try {
      final cfg = await widget.client.agentConfig();
      if (!mounted) return;
      _apply(cfg);
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
      final saved = await widget.client.saveAgentConfig(_yaml.text, ifUpdated: _updated);
      if (!mounted) return;
      _apply(saved);
      _notice = '已写入配置文件。重启本机服务后才会生效。';
    } on ApiException catch (e) {
      if (!mounted) return;
      _error = e.message;
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }

  Future<void> _saveForm() async {
    final form = _form;
    if (!_writable || _busy || form == null) return;
    form['hostname'] = _host.text;
    form['listen'] = _listen.text;
    form['health_silent'] = _silent;
    final notify = form['notify'];
    if (notify is Map) {
      notify['slack_webhook_url'] = _slack.text;
    }
    final targets = form['targets'];
    if (targets is List) {
      for (final item in targets) {
        if (item is Map && item['name'] == 'nginx') item['url'] = _nginx.text;
      }
    }
    setState(() {
      _busy = true;
      _error = '';
      _notice = '';
    });
    try {
      final saved = await widget.client.saveAgentForm(form, ifUpdated: _updated);
      if (!mounted) return;
      _apply(saved);
      _notice = '已写入配置文件。重启本机服务后才会生效。';
    } on ApiException catch (e) {
      if (!mounted) return;
      _error = e.message;
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }

  Future<void> _rollback() async {
    if (_busy || _backup.isEmpty) return;
    final ok = await showDialog<bool>(
      context: context,
      builder: (context) => AlertDialog(
        title: const Text('恢复上一份'),
        content: const Text('当前文件会变成新的备份。'),
        actions: [
          TextButton(onPressed: () => Navigator.pop(context, false), child: const Text('取消')),
          TextButton(onPressed: () => Navigator.pop(context, true), child: const Text('恢复')),
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
      final saved = await widget.client.rollbackAgentConfig();
      if (!mounted) return;
      _apply(saved);
      _notice = '已恢复上一份配置。重启本机服务后才会生效。';
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
        if (_form != null) ...[
          const SizedBox(height: 8),
          TextField(controller: _host, readOnly: !_writable, decoration: const InputDecoration(labelText: '主机名')),
          TextField(controller: _listen, readOnly: !_writable, decoration: const InputDecoration(labelText: '监听地址')),
          TextField(controller: _nginx, readOnly: !_writable, decoration: const InputDecoration(labelText: 'nginx 地址')),
          TextField(controller: _slack, readOnly: !_writable, obscureText: true, decoration: const InputDecoration(labelText: 'Slack 地址')),
          SwitchListTile(
            contentPadding: EdgeInsets.zero,
            title: const Text('只评估，不发送通知'),
            value: _silent,
            onChanged: !_writable || _busy ? null : (v) => setState(() => _silent = v),
          ),
          FilledButton(onPressed: _writable && !_busy ? _saveForm : null, child: const Text('保存表单')),
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
            OutlinedButton(onPressed: _backup.isEmpty || _busy ? null : _rollback, child: const Text('恢复上一份')),
            const SizedBox(width: 8),
            OutlinedButton(onPressed: _busy ? null : _restart, child: const Text('重启服务')),
          ],
        ),
      ],
    );
  }
}
