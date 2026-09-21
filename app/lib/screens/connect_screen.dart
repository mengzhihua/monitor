import 'package:flutter/material.dart';

import '../api/client.dart';
import '../state/app_state.dart';

class ConnectScreen extends StatefulWidget {
  const ConnectScreen({super.key, required this.state});

  final AppState state;

  @override
  State<ConnectScreen> createState() => _ConnectScreenState();
}

class _ConnectScreenState extends State<ConnectScreen> {
  final _url = TextEditingController(text: 'http://127.0.0.1:19999');
  final _token = TextEditingController();
  final _form = GlobalKey<FormState>();

  @override
  void dispose() {
    _url.dispose();
    _token.dispose();
    super.dispose();
  }

  Future<void> _submit() async {
    if (!_form.currentState!.validate()) return;
    final url = _url.text.trim().replaceAll(RegExp(r'/+$'), '');
    await widget.state
        .connect(ServerConfig(baseUrl: url, token: _token.text.trim()));
  }

  @override
  Widget build(BuildContext context) {
    final st = widget.state;
    return Scaffold(
      body: Center(
        child: ConstrainedBox(
          constraints: const BoxConstraints(maxWidth: 420),
          child: Padding(
            padding: const EdgeInsets.all(24),
            child: Form(
              key: _form,
              child: Column(
                mainAxisSize: MainAxisSize.min,
                crossAxisAlignment: CrossAxisAlignment.stretch,
                children: [
                  const Icon(Icons.monitor_heart, size: 64),
                  const SizedBox(height: 8),
                  Text('Monitor',
                      textAlign: TextAlign.center,
                      style: Theme.of(context).textTheme.headlineMedium),
                  const SizedBox(height: 24),
                  TextFormField(
                    controller: _url,
                    keyboardType: TextInputType.url,
                    decoration: const InputDecoration(
                      labelText: 'Server URL',
                      hintText: 'http://host:19999',
                      border: OutlineInputBorder(),
                    ),
                    validator: (v) {
                      final u = Uri.tryParse((v ?? '').trim());
                      if (u == null || !u.hasScheme || u.host.isEmpty) {
                        return 'Enter a http(s) URL';
                      }
                      return null;
                    },
                  ),
                  const SizedBox(height: 12),
                  TextFormField(
                    controller: _token,
                    obscureText: true,
                    decoration: const InputDecoration(
                      labelText: 'API token (optional)',
                      border: OutlineInputBorder(),
                    ),
                    onFieldSubmitted: (_) => _submit(),
                  ),
                  const SizedBox(height: 16),
                  if (st.error != null)
                    Padding(
                      padding: const EdgeInsets.only(bottom: 12),
                      child: Text(st.error!,
                          style: TextStyle(
                              color: Theme.of(context).colorScheme.error)),
                    ),
                  FilledButton(
                    onPressed: st.loading ? null : _submit,
                    child: st.loading
                        ? const SizedBox(
                            height: 18,
                            width: 18,
                            child: CircularProgressIndicator(strokeWidth: 2))
                        : const Text('Connect'),
                  ),
                ],
              ),
            ),
          ),
        ),
      ),
    );
  }
}
