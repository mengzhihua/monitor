import 'dart:async';
import 'dart:convert';

import 'package:http/http.dart' as http;
import 'package:web_socket_channel/web_socket_channel.dart';

import 'models.dart';

class ApiException implements Exception {
  ApiException(this.status, this.message);

  final int status;
  final String message;

  @override
  String toString() => 'HTTP $status: $message';
}

/// Connection settings for one monitord (agent or hub).
class ServerConfig {
  const ServerConfig({required this.baseUrl, this.token = ''});

  /// e.g. `http://192.168.1.10:19999`
  final String baseUrl;
  final String token;

  Uri uri(String path, [Map<String, String> query = const {}]) {
    final base = Uri.parse(
      baseUrl.endsWith('/')
          ? baseUrl.substring(0, baseUrl.length - 1)
          : baseUrl,
    );
    return base.replace(
      path: '${base.path}$path',
      queryParameters: query.isEmpty ? null : query,
    );
  }

  Uri wsUri(String path, [Map<String, String> query = const {}]) {
    final u = uri(path, query);
    return u.replace(scheme: u.scheme == 'https' ? 'wss' : 'ws');
  }

  List<String> get liveProtocols => [
    'monitor',
    if (token.isNotEmpty)
      'bearer.${base64Url.encode(utf8.encode(token)).replaceAll("=", "")}',
  ];

  Map<String, String> get headers =>
      token.isEmpty ? const {} : {'Authorization': 'Bearer $token'};
}

/// Thin typed wrapper over `/api/v1`; every call accepts an optional
/// `node` selector (`local` or a hub node id).
class ApiClient {
  ApiClient(this.config, {http.Client? httpClient})
    : _http = httpClient ?? http.Client();

  final ServerConfig config;
  final http.Client _http;

  Future<Map<String, dynamic>> _get(
    String path, [
    Map<String, String> q = const {},
  ]) async {
    final res = await _http
        .get(config.uri('/api/v1$path', q), headers: config.headers)
        .timeout(const Duration(seconds: 15));
    if (res.statusCode != 200) {
      throw ApiException(res.statusCode, res.body.trim());
    }
    return jsonDecode(res.body) as Map<String, dynamic>;
  }

  Map<String, String> _q(
    String? node, [
    Map<String, String> extra = const {},
  ]) => {if (node != null && node.isNotEmpty) 'node': node, ...extra};

  Future<ServerInfo> info() async => ServerInfo.fromJson(await _get('/info'));

  Future<List<Node>> nodes() async {
    final j = await _get('/nodes');
    return ((j['nodes'] as List<dynamic>?) ?? const [])
        .map((n) => Node.fromJson(n as Map<String, dynamic>))
        .toList();
  }

  Future<List<Chart>> charts({String? node}) async {
    final j = await _get('/charts', _q(node));
    final charts = (j['charts'] as Map<String, dynamic>?) ?? const {};
    final list = charts.values
        .map((c) => Chart.fromJson(c as Map<String, dynamic>))
        .toList();
    list.sort((a, b) {
      final p = a.priority.compareTo(b.priority);
      return p != 0 ? p : a.id.compareTo(b.id);
    });
    return list;
  }

  Future<ChartData> data(
    String chart, {
    String? node,
    int afterSeconds = 300,
    int points = 300,
  }) async {
    final j = await _get(
      '/data',
      _q(node, {
        'chart': chart,
        'after': '-$afterSeconds',
        'points': '$points',
        'group': 'average',
      }),
    );
    return ChartData.fromJson(j);
  }

  Future<List<Alarm>> alarms({String? node, bool all = false}) async {
    final j = await _get('/alarms', _q(node, {if (all) 'all': 'true'}));
    final m = (j['alarms'] as Map<String, dynamic>?) ?? const {};
    final list = m.values
        .map((a) => Alarm.fromJson(a as Map<String, dynamic>))
        .toList();
    list.sort((a, b) => b.lastStatusChange.compareTo(a.lastStatusChange));
    return list;
  }

  Future<List<AlarmLogEntry>> alarmLog({String? node}) async {
    final res = await _http
        .get(config.uri('/api/v1/alarm_log', _q(node)), headers: config.headers)
        .timeout(const Duration(seconds: 15));
    if (res.statusCode != 200) {
      throw ApiException(res.statusCode, res.body.trim());
    }
    final list = jsonDecode(res.body) as List<dynamic>;
    return list
        .map((e) => AlarmLogEntry.fromJson(e as Map<String, dynamic>))
        .toList()
        .reversed
        .toList();
  }

  /// Subscribe to live samples for [charts] on [node]. The returned stream
  /// closes when the socket closes; callers own reconnect policy.
  LiveSubscription live({required List<String> charts, String? node}) {
    final ch = WebSocketChannel.connect(
      config.wsUri('/api/v1/live', {
        if (node != null && node.isNotEmpty) 'node': node,
      }),
      protocols: config.liveProtocols,
    );
    ch.sink.add(jsonEncode({'charts': charts}));
    return LiveSubscription._(ch);
  }

  Future<List<FunctionInfo>> functions({String? node}) async {
    final res = await _http
        .get(config.uri('/api/v1/functions', _q(node)), headers: config.headers)
        .timeout(const Duration(seconds: 15));
    if (res.statusCode != 200) {
      throw ApiException(res.statusCode, res.body.trim());
    }
    final list = jsonDecode(res.body) as List<dynamic>;
    return list
        .map((e) => FunctionInfo.fromJson(e as Map<String, dynamic>))
        .toList();
  }

  Future<FunctionResult> function(
    String name, {
    String? node,
    Map<String, String> args = const {},
  }) async {
    final j = await _get('/function', _q(node, {'function': name, ...args}));
    return FunctionResult.fromJson(j);
  }

  void close() => _http.close();
}

class LiveSubscription {
  LiveSubscription._(this._ch);

  final WebSocketChannel _ch;

  Stream<LiveSample> get samples => _ch.stream
      .map((m) => jsonDecode(m as String))
      .where((j) => j is Map<String, dynamic>)
      .map((j) => LiveSample.tryParse(j as Map<String, dynamic>))
      .where((s) => s != null)
      .cast<LiveSample>();

  void setCharts(List<String> charts) =>
      _ch.sink.add(jsonEncode({'charts': charts}));

  Future<void> close() => _ch.sink.close();
}
