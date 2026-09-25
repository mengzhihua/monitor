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
  final _slack = TextEditingController();
  final _nginx = TextEditingController();
  Map<String, dynamic>? _form;
  String _path = '';
  String _notice = '';
  String _error = '';
  bool _writable = false;
  bool _restartRequired = false;
  bool _backup = false;
  bool _busy = false;

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
    _slack.dispose();
    _nginx.dispose();
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
      final rawForm = cfg['form'];
      _form = rawForm is Map ? Map<String, dynamic>.from(rawForm) : null;
      _host.text = (_form?['hostname'] as String?) ?? '';
      _listen.text = (_form?['listen'] as String?) ?? '';
      final notify = _form?['notify'];
      _slack.text = notify is Map ? (notify['slack_webhook_url'] as String?) ?? '' : '';
      _nginx.text = '';
      final targets = _form?['targets'];
      if (targets is List) {
        for (final item in targets) {
          if (item is Map && item['name'] == 'nginx') {
            _nginx.text = (item['url'] as String?) ?? '';
          }
        }
      }
      _writable = cfg['writable'] == true;
      _restartRequired = cfg['restart_required'] == true;
      _backup = cfg['backup'] == true;
    } on ApiException catch (e) {
      if (!mounted) return;
      _error = e.message;
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }

  Future<void> _saveForm() async {
    final form = _form;
    if (form == null || !_writable || _busy) return;
    form['hostname'] = _host.text;
    form['listen'] = _listen.text;
    final notify = form['notify'];
    if (notify is Map) notify['slack_webhook_url'] = _slack.text;
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
      final cfg = await widget.client.saveAgentForm(form);
      if (!mounted) return;
      _yaml.text = (cfg['yaml'] as String?) ?? _yaml.text;
      _notice = '已写入表单配置。重启本机服务后才会生效。';
      _restartRequired = cfg['restart_required'] == true;
      _backup = cfg['backup'] == true;
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
      _restartRequired = true;
      _backup = true;
      _notice = '已写入配置文件。重启本机服务后才会生效。';
    } on ApiException catch (e) {
      if (!mounted) return;
      _error = e.message;
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }

  Future<void> _rollback() async {
    if (!_backup || _busy) return;
    final ok = await showDialog<bool>(
      context: context,
      builder: (context) => AlertDialog(
        title: const Text('恢复上一份'),
        content: const Text('用上一份配置覆盖当前文件。'),
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
      final cfg = await widget.client.rollbackAgentConfig();
      if (!mounted) return;
      _yaml.text = (cfg['yaml'] as String?) ?? '';
      _restartRequired = cfg['restart_required'] == true;
      _backup = cfg['backup'] == true;
      _notice = '已恢复上一份配置。';
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
        const Text('表单只改常用项。告警规则、采集器参数和 web.token 会保留。'),
        if (_form != null) ...[
          const SizedBox(height: 8),
          DropdownButtonFormField<String>(
            value: (_form!['mode'] as String?) ?? 'agent',
            decoration: const InputDecoration(labelText: '运行模式'),
            items: const [
              DropdownMenuItem(value: 'agent', child: Text('Agent')),
              DropdownMenuItem(value: 'hub', child: Text('Hub')),
            ],
            onChanged: _writable ? (v) => _form!['mode'] = v : null,
          ),
          TextField(controller: _host, decoration: const InputDecoration(labelText: '主机名'), readOnly: !_writable),
          TextField(controller: _listen, decoration: const InputDecoration(labelText: '监听地址'), readOnly: !_writable),
          TextField(controller: _slack, decoration: const InputDecoration(labelText: 'Slack Webhook'), readOnly: !_writable, obscureText: true),
          TextField(controller: _nginx, decoration: const InputDecoration(labelText: 'nginx 地址'), readOnly: !_writable),
          SwitchListTile(
            contentPadding: EdgeInsets.zero,
            title: const Text('只评估，不发送通知'),
            value: _form!['health_silent'] == true,
            onChanged: _writable ? (v) => setState(() => _form!['health_silent'] = v) : null,
          ),
          FilledButton(onPressed: _writable && !_busy ? _saveForm : null, child: const Text('保存表单')),
        ],
        if (_error.isNotEmpty) ...[
          const SizedBox(height: 8),
          Text(_error, style: TextStyle(color: Theme.of(context).colorScheme.error)),
        ],
        if (_restartRequired) ...[
          const SizedBox(height: 8),
          const Text('磁盘上的配置与当前进程不一致，重启后才会生效。'),
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
            OutlinedButton(onPressed: _backup && !_busy ? _rollback : null, child: const Text('恢复上一份')),
            const SizedBox(width: 8),
            OutlinedButton(onPressed: _busy ? null : _restart, child: const Text('重启服务')),
          ],
        ),
      ],
    );
  }
}
