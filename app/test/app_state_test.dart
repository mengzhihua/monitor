import 'dart:async';

import 'package:flutter_test/flutter_test.dart';
import 'package:shared_preferences/shared_preferences.dart';
import 'package:monitor_app/api/client.dart';
import 'package:monitor_app/api/models.dart';
import 'package:monitor_app/state/app_state.dart';
import 'package:monitor_app/state/credential_store.dart';

const first = ServerConfig(
  baseUrl: 'https://first',
  token: 'first-test-password',
);
const second = ServerConfig(
  baseUrl: 'https://second',
  token: 'second-test-password',
);

class _Client extends ApiClient {
  _Client(super.config);
  Completer<ServerInfo>? infoGate;
  Completer<List<Node>>? nodesGate;
  bool rejectInfo = false;
  int closes = 0;
  int nodeCalls = 0;
  List<Node> nodeList = [];

  @override
  Future<ServerInfo> info() async {
    if (rejectInfo) throw ApiException(401, 'invalid password');
    return infoGate?.future ??
        ServerInfo.fromJson({
          'mode': 'hub',
          'host': {'hostname': config.baseUrl},
        });
  }

  @override
  Future<List<Node>> nodes() async {
    nodeCalls++;
    return nodesGate?.future ?? nodeList;
  }

  @override
  void close() {
    closes++;
    super.close();
  }
}

class _Credentials extends CredentialStore {
  ServerConfig? saved;
  Completer<void>? saveGate;
  Completer<void>? loadGate;
  final saveStarted = Completer<void>();
  final loadStarted = Completer<void>();
  final events = <String>[];
  bool failSave = false;
  bool failClear = false;

  @override
  Future<String> load(String url) async {
    if (!loadStarted.isCompleted) loadStarted.complete();
    await loadGate?.future;
    return saved?.baseUrl == url ? saved!.token : '';
  }

  @override
  Future<void> save(ServerConfig config, {required bool remember}) async {
    if (!saveStarted.isCompleted) saveStarted.complete();
    await saveGate?.future;
    if (failSave) throw StateError('keychain unavailable');
    saved = remember ? config : null;
    events.add('save:${config.baseUrl}');
  }

  @override
  Future<void> clear() async {
    if (failClear) throw StateError('keychain unavailable');
    saved = null;
    events.add('clear');
  }
}

Node _node(String id) => Node.fromJson({'id': id, 'hostname': id});

class _RejectedPreferences implements SharedPreferences {
  @override
  Future<bool> setString(String key, String value) async => false;
  @override
  dynamic noSuchMethod(Invocation invocation) => super.noSuchMethod(invocation);
}

void main() {
  TestWidgetsFlutterBinding.ensureInitialized();
  setUp(() => SharedPreferences.setMockInitialValues({}));

  test(
    'rejected preference writes are reported without breaking the session',
    () async {
      final client = _Client(first);
      final state = AppState(
        clientFactory: (_) => client,
        credentials: _Credentials(),
        preferences: () async => _RejectedPreferences(),
      );
      addTearDown(state.dispose);
      expect(await state.connect(first), isTrue);
      expect(state.storageWarning, contains('could not be saved'));
      expect(client.closes, 0);
    },
  );

  test(
    'a previous client refresh does not block refreshes on a new connection',
    () async {
      final old = _Client(first);
      final next = _Client(second)..infoGate = Completer<ServerInfo>();
      final state = AppState(
        clientFactory: (cfg) => cfg == first ? old : next,
        credentials: _Credentials(),
      );
      addTearDown(state.dispose);
      await state.connect(first);
      final switching = state.connect(second);
      old.nodesGate = Completer<List<Node>>();
      final oldRefresh = state.refreshNodes();
      next.infoGate!.complete(ServerInfo.fromJson({}));
      expect(await switching, isTrue);
      await state.refreshNodes();
      expect(next.nodeCalls, 2);
      old.nodesGate!.complete([_node('old')]);
      await oldRefresh;
      expect(state.nodes, isEmpty);
      await state.refreshNodes();
      expect(next.nodeCalls, 3);
    },
  );

  test(
    'disconnect invalidates a pending login and closes its client immediately',
    () async {
      final client = _Client(first)..infoGate = Completer<ServerInfo>();
      final state = AppState(
        clientFactory: (_) => client,
        credentials: _Credentials(),
      );
      addTearDown(state.dispose);
      var notifications = 0;
      state.addListener(() => notifications++);
      final connecting = state.connect(first);
      expect(state.loading, isTrue);
      await state.disconnect();
      final afterDisconnect = notifications;
      expect(client.closes, 1);
      expect(state.connected, isFalse);
      client.infoGate!.complete(ServerInfo.fromJson({}));
      expect(await connecting, isFalse);
      expect(state.connected, isFalse);
      expect(state.loading, isFalse);
      expect(client.nodeCalls, 0);
      expect(notifications, afterDisconnect);
    },
  );

  test('failed replacement leaves the previous client usable', () async {
    final active = _Client(first);
    final rejected = _Client(second)..rejectInfo = true;
    final state = AppState(
      clientFactory: (cfg) => cfg == first ? active : rejected,
      credentials: _Credentials(),
    );
    addTearDown(state.dispose);
    expect(await state.connect(first), isTrue);
    expect(await state.connect(second), isFalse);
    expect(state.client, same(active));
    expect(state.config, first);
    expect(state.info!.hostname, first.baseUrl);
    expect(active.closes, 0);
    expect(rejected.closes, 1);
    expect(state.connected, isTrue);
  });

  test(
    'preference failures keep a validated connection open for the session',
    () async {
      final client = _Client(first);
      final state = AppState(
        clientFactory: (_) => client,
        credentials: _Credentials(),
        preferences: () => Future.error(StateError('preferences unavailable')),
      );
      addTearDown(state.dispose);
      expect(await state.connect(first), isTrue);
      expect(state.connected, isTrue);
      expect(client.closes, 0);
      expect(state.storageWarning, contains('could not be saved'));
    },
  );

  test('secure-storage warning survives successful node refreshes', () async {
    final client = _Client(first);
    final credentials = _Credentials()..failSave = true;
    final state = AppState(
      clientFactory: (_) => client,
      credentials: credentials,
    );
    addTearDown(state.dispose);
    expect(await state.connect(first), isTrue);
    final warning = state.storageWarning;
    expect(warning, contains('Secure storage'));
    await state.refreshNodes();
    expect(state.storageWarning, warning);
    expect(state.connected, isTrue);
  });

  test(
    'logout clears a late password save before the next login is persisted',
    () async {
      final old = _Client(first);
      final next = _Client(second);
      final credentials = _Credentials()..saveGate = Completer<void>();
      final state = AppState(
        clientFactory: (cfg) => cfg == first ? old : next,
        credentials: credentials,
      );
      addTearDown(state.dispose);
      final oldLogin = state.connect(first);
      await credentials.saveStarted.future;
      final logout = state.disconnect();
      expect(state.connected, isFalse);
      expect(state.loading, isFalse);
      final newLogin = state.connect(second);
      credentials.saveGate!.complete();
      expect(await oldLogin, isFalse);
      await logout;
      expect(await newLogin, isTrue);
      expect(credentials.events, [
        'save:https://first',
        'clear',
        'save:https://second',
      ]);
      expect(credentials.saved, second);
      final prefs = await SharedPreferences.getInstance();
      expect(prefs.getString('server.url'), second.baseUrl);
      expect(prefs.containsKey('server.token'), isFalse);
      expect(state.client, same(next));
      expect(old.closes, 1);
      expect(next.closes, 0);
    },
  );

  test(
    'logout alone clears a credential write that was already in progress',
    () async {
      final client = _Client(first);
      final credentials = _Credentials()..saveGate = Completer<void>();
      final state = AppState(
        clientFactory: (_) => client,
        credentials: credentials,
      );
      addTearDown(state.dispose);
      final login = state.connect(first);
      await credentials.saveStarted.future;
      final logout = state.disconnect();
      credentials.saveGate!.complete();
      await logout;
      expect(await login, isFalse);
      expect(credentials.saved, isNull);
      final prefs = await SharedPreferences.getInstance();
      expect(prefs.getString('server.url'), isNull);
      expect(prefs.getString('server.node'), isNull);
    },
  );

  test('old node requests cannot repopulate state after disconnect', () async {
    final client = _Client(first);
    final state = AppState(
      clientFactory: (_) => client,
      credentials: _Credentials(),
    );
    addTearDown(state.dispose);
    await state.connect(first);
    client.nodesGate = Completer<List<Node>>();
    final refresh = state.refreshNodes();
    await state.disconnect();
    client.nodesGate!.complete([_node('obsolete')]);
    await refresh;
    expect(state.nodes, isEmpty);
    expect(state.connected, isFalse);
    expect(state.error, isNull);
  });

  test('cleanup failure remains visible if a newer login also fails', () async {
    final old = _Client(first);
    final next = _Client(second)..infoGate = Completer<ServerInfo>();
    final credentials = _Credentials();
    final state = AppState(
      clientFactory: (cfg) => cfg == first ? old : next,
      credentials: credentials,
    );
    addTearDown(state.dispose);
    await state.connect(first);
    credentials.failClear = true;
    final logout = state.disconnect();
    final login = state.connect(second);
    await logout;
    next.infoGate!.completeError(ApiException(401, 'invalid password'));
    expect(await login, isFalse);
    expect(state.connected, isFalse);
    expect(credentials.saved, first);
    expect(state.error, contains('invalid password'));
    expect(
      state.storageWarning,
      contains('Could not remove the saved credential'),
    );
    next.infoGate = null;
    expect(await state.connect(second), isTrue);
    expect(credentials.saved, second);
    expect(state.storageWarning, isNull);
  });

  test(
    'node refreshes share one in-flight request and fallback is persisted',
    () async {
      final client = _Client(first)..nodeList = [_node('remote')];
      final state = AppState(
        clientFactory: (_) => client,
        credentials: _Credentials(),
      );
      addTearDown(state.dispose);
      await state.connect(first);
      await state.selectNode('remote');
      client.nodesGate = Completer<List<Node>>();
      final a = state.refreshNodes();
      final b = state.refreshNodes();
      expect(identical(a, b), isTrue);
      expect(client.nodeCalls, 2);
      client.nodesGate!.complete([]);
      await Future.wait([a, b]);
      expect(state.selectedNode, 'local');
      expect(
        (await SharedPreferences.getInstance()).getString('server.node'),
        'local',
      );
    },
  );

  test('a slow restore cannot replace a newer explicit connection', () async {
    SharedPreferences.setMockInitialValues({
      'server.url': first.baseUrl,
      'server.node': 'old',
    });
    final credentials = _Credentials()
      ..saved = first
      ..loadGate = Completer<void>();
    final client = _Client(second);
    final requested = <ServerConfig>[];
    final state = AppState(
      clientFactory: (config) {
        requested.add(config);
        return client;
      },
      credentials: credentials,
    );
    addTearDown(state.dispose);
    final restoring = state.restore();
    await credentials.loadStarted.future;
    expect(await state.connect(second), isTrue);
    credentials.loadGate!.complete();
    await restoring;
    expect(requested, [second]);
    expect(state.config, second);
  });

  test(
    'disposing during a request closes clients without late notifications',
    () async {
      final client = _Client(first);
      final state = AppState(
        clientFactory: (_) => client,
        credentials: _Credentials(),
      );
      await state.connect(first);
      client.nodesGate = Completer<List<Node>>();
      final refreshing = state.refreshNodes();
      state.dispose();
      client.nodesGate!.completeError(StateError('request canceled'));
      await refreshing;
      expect(client.closes, 1);
    },
  );
}
