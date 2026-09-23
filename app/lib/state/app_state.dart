import 'package:flutter/foundation.dart';
import 'package:shared_preferences/shared_preferences.dart';

import '../api/client.dart';
import '../api/models.dart';
import 'credential_store.dart';

/// Credentials persist only in the operating system secure store.
class AppState extends ChangeNotifier {
  AppState({
    ApiClient Function(ServerConfig)? clientFactory,
    CredentialStore? credentials,
    Future<SharedPreferences> Function()? preferences,
  }) : _clientFactory = clientFactory ?? ((config) => ApiClient(config)),
       _credentials = credentials ?? CredentialStore(),
       _preferences = preferences ?? SharedPreferences.getInstance;

  static const _kUrl = 'server.url';
  static const _kToken = 'server.token';
  static const _kNode = 'server.node';

  final ApiClient Function(ServerConfig) _clientFactory;
  final CredentialStore _credentials;
  final Future<SharedPreferences> Function() _preferences;
  ServerConfig? _config;
  ApiClient? _client;
  ApiClient? _candidate;
  Future<void>? _nodesPending;
  Future<void> _storagePending = Future.value();
  int _generation = 0;
  bool _disposed = false;
  ServerInfo? info;
  List<Node> nodes = const [];
  String selectedNode = 'local';
  String? error;
  String? storageWarning;
  bool loading = false;

  ServerConfig? get config => _config;
  ApiClient? get client => _client;
  bool get connected => _client != null && info != null;

  bool _current(int generation) => !_disposed && generation == _generation;
  void _notify() {
    if (!_disposed) notifyListeners();
  }

  // A logout queued behind an in-flight keychain save must clear that save;
  // a later login must then be saved after logout has finished clearing.
  Future<void> _storage(Future<void> Function() action) {
    final operation = _storagePending.then((_) => action());
    _storagePending = operation.catchError((Object _) {});
    return operation;
  }

  Future<void> _checked(Future<bool> operation) async {
    if (!await operation) {
      throw StateError('Connection settings were not persisted.');
    }
  }

  Node? get currentNode {
    for (final node in nodes) {
      if (node.selector == selectedNode) return node;
    }
    return null;
  }

  Future<void> restore() async {
    if (_disposed || loading || connected) return;
    final generation = ++_generation;
    try {
      final prefs = await _preferences();
      if (!_current(generation)) return;
      final url = prefs.getString(_kUrl);
      final node = prefs.getString(_kNode) ?? 'local';
      await _checked(prefs.remove(_kToken));
      if (!_current(generation) || url == null || url.isEmpty) return;
      final token = await _credentials.load(url);
      if (!_current(generation)) return;
      await _connect(
        ServerConfig(baseUrl: url, token: token),
        remember: true,
        generation: generation,
        preferredNode: node,
      );
    } catch (_) {
      if (!_current(generation)) return;
      error =
          'Saved connection unavailable. Enter the server URL and password to connect.';
      _notify();
    }
  }

  Future<bool> connect(ServerConfig config, {bool remember = true}) {
    if (_disposed || loading) return Future.value(false);
    return _connect(config, remember: remember, generation: ++_generation);
  }

  Future<bool> _connect(
    ServerConfig config, {
    required bool remember,
    required int generation,
    String preferredNode = 'local',
  }) async {
    loading = true;
    error = null;
    _nodesPending = null;
    _notify();
    if (!_current(generation)) return false;
    ApiClient? candidate;
    try {
      candidate = _clientFactory(config);
      _candidate = candidate;
      final nextInfo = await candidate.info();
      if (!_current(generation)) return false;
      var nextNodes = <Node>[];
      String? nodeError;
      try {
        nextNodes = await candidate.nodes();
      } catch (e) {
        nodeError = e.toString();
      }
      if (!_current(generation)) return false;
      final nextNode = nextNodes.any((node) => node.selector == preferredNode)
          ? preferredNode
          : 'local';
      String? warning;
      await _storage(() async {
        if (!_current(generation)) return;
        try {
          final prefs = await _preferences();
          if (!_current(generation)) return;
          await _checked(prefs.setString(_kUrl, config.baseUrl));
          await _checked(prefs.remove(_kToken));
          await _checked(prefs.setString(_kNode, nextNode));
        } catch (_) {
          warning =
              'Connected for this session. Connection settings could not be saved.';
        }
        if (!_current(generation)) return;
        try {
          await _credentials.save(config, remember: remember);
        } catch (_) {
          warning = remember
              ? 'Connected. Secure storage could not confirm the saved password; you may need to enter it again.'
              : 'Connected for this session, but previously saved credentials could not be removed.';
        }
      });
      if (!_current(generation)) return false;
      // Publish one complete connection. A failed candidate never replaces the
      // working client, and storage failures do not close a valid connection.
      _client?.close();
      _client = candidate;
      _nodesPending = null;
      _candidate = null;
      _config = config;
      info = nextInfo;
      nodes = nextNodes;
      selectedNode = nextNode;
      error = nodeError;
      storageWarning = warning;
      return true;
    } catch (e) {
      if (_current(generation)) error = e.toString();
      return false;
    } finally {
      if (candidate != null && identical(_candidate, candidate)) {
        _candidate = null;
        candidate.close();
      }
      if (_current(generation)) {
        loading = false;
        _notify();
      }
    }
  }

  Future<void> refreshNodes() {
    final active = _client;
    if (_disposed || active == null) return Future.value();
    if (_nodesPending != null) return _nodesPending!;
    final generation = _generation;
    return _nodesPending = _readNodes(active, generation).whenComplete(() {
      if (_current(generation) && identical(active, _client)) {
        _nodesPending = null;
      }
    });
  }

  Future<void> _readNodes(ApiClient active, int generation) async {
    try {
      final result = await active.nodes();
      if (!_current(generation) || !identical(active, _client)) return;
      nodes = result;
      final fallback = currentNode == null && selectedNode != 'local';
      if (fallback) selectedNode = 'local';
      error = null;
      _notify();
      if (fallback) await _persistNode('local', generation);
    } catch (e) {
      if (!_current(generation) || !identical(active, _client)) return;
      error = e.toString();
      _notify();
    }
  }

  Future<void> selectNode(String selector) async {
    if (_disposed || !connected || selectedNode == selector) return;
    if (selector != 'local' &&
        !nodes.any((node) => node.selector == selector)) {
      return;
    }
    final generation = _generation;
    selectedNode = selector;
    _notify();
    await _persistNode(selector, generation);
  }

  Future<void> _persistNode(String selector, int generation) async {
    try {
      await _storage(() async {
        if (!_current(generation) || selectedNode != selector) return;
        final prefs = await _preferences();
        if (!_current(generation) || selectedNode != selector) return;
        await _checked(prefs.setString(_kNode, selector));
      });
    } catch (_) {
      if (!_current(generation)) return;
      storageWarning = 'The selected node could not be saved.';
      _notify();
    }
  }

  Future<void> disconnect() async {
    if (_disposed) return;
    final generation = ++_generation;
    _candidate?.close();
    _candidate = null;
    _client?.close();
    _client = null;
    _config = null;
    _nodesPending = null;
    info = null;
    nodes = const [];
    selectedNode = 'local';
    loading = false;
    error = null;
    storageWarning = null;
    _notify();
    // Cleanup is ordered even if a new connect starts while it is pending.
    await _storage(() async {
      String? warning;
      try {
        final prefs = await _preferences();
        await Future.wait([
          _checked(prefs.remove(_kUrl)),
          _checked(prefs.remove(_kToken)),
          _checked(prefs.remove(_kNode)),
        ]);
      } catch (_) {
        warning = 'Could not remove the saved connection settings.';
      }
      try {
        await _credentials.clear();
      } catch (_) {
        warning = 'Could not remove the saved credential from secure storage.';
      }
      if (!_disposed && warning != null) {
        // A newer login may still fail before reaching secure storage. Keep
        // cleanup failures visible until a subsequent save actually succeeds.
        storageWarning = warning;
        if (_current(generation)) error = warning;
        _notify();
      }
    });
  }

  @override
  void dispose() {
    _disposed = true;
    ++_generation;
    _candidate?.close();
    _candidate = null;
    _client?.close();
    _client = null;
    super.dispose();
  }
}
