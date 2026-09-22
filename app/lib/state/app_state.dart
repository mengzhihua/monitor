import 'package:flutter/foundation.dart';
import 'package:shared_preferences/shared_preferences.dart';

import '../api/client.dart';
import '../api/models.dart';
import 'credential_store.dart';

/// Credentials persist only in the operating system secure store.
class AppState extends ChangeNotifier {
  static const _kUrl = 'server.url';
  static const _kToken = 'server.token';
  static const _kNode = 'server.node';

  final _credentials = CredentialStore();
  ServerConfig? _config;
  ApiClient? _client;
  ServerInfo? info;
  List<Node> nodes = const [];
  String selectedNode = 'local';
  String? error;
  bool loading = false;

  ServerConfig? get config => _config;
  ApiClient? get client => _client;
  bool get connected => _client != null && info != null;

  Node? get currentNode {
    for (final n in nodes) {
      if (n.selector == selectedNode) return n;
    }
    return null;
  }

  Future<void> restore() async {
    final p = await SharedPreferences.getInstance();
    final url = p.getString(_kUrl);
    await p.remove(_kToken);
    if (url == null || url.isEmpty) return;
    selectedNode = p.getString(_kNode) ?? 'local';
    await p.remove(_kToken); // Remove credentials saved by earlier versions.
    try {
      final token = await _credentials.load(url);
      await connect(ServerConfig(baseUrl: url, token: token));
    } catch (_) {
      error = 'Secure storage is unavailable. Enter your token to connect.';
      notifyListeners();
    }
  }

  Future<bool> connect(ServerConfig cfg, {bool remember = true}) async {
    if (loading) return false;
    loading = true;
    error = null;
    notifyListeners();
    final c = ApiClient(cfg);
    try {
      final i = await c.info();
      _client?.close();
      _client = c;
      _config = cfg;
      info = i;
      final p = await SharedPreferences.getInstance();
      await p.setString(_kUrl, cfg.baseUrl);
      await p.remove(_kToken);
      await refreshNodes();
      try {
        await _credentials.save(cfg, remember: remember);
      } catch (_) {
        error =
            'Connected for this session. Secure storage is unavailable; the token was not saved.';
      }
      return true;
    } catch (e) {
      c.close();
      error = e.toString();
      return false;
    } finally {
      loading = false;
      notifyListeners();
    }
  }

  Future<void> refreshNodes() async {
    final c = _client;
    if (c == null) return;
    try {
      nodes = await c.nodes();
      if (currentNode == null) selectedNode = 'local';
      error = null;
    } catch (e) {
      error = e.toString();
    }
    notifyListeners();
  }

  Future<void> selectNode(String selector) async {
    selectedNode = selector;
    notifyListeners();
    final p = await SharedPreferences.getInstance();
    await p.setString(_kNode, selector);
  }

  Future<void> disconnect() async {
    _client?.close();
    _client = null;
    _config = null;
    info = null;
    nodes = const [];
    selectedNode = 'local';
    final p = await SharedPreferences.getInstance();
    await p.remove(_kUrl);
    await p.remove(_kToken);
    await p.remove(_kNode);
    try {
      await _credentials.clear();
    } catch (_) {
      error = "Could not remove the saved credential from secure storage.";
    }
    notifyListeners();
  }
}
