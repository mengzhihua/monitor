import 'package:flutter_test/flutter_test.dart';
import 'package:monitor_app/api/client.dart';
import 'package:monitor_app/api/models.dart';
import 'package:monitor_app/version.dart';

void main() {
  test('ServerConfig builds http and ws URIs with token', () {
    const cfg = ServerConfig(baseUrl: 'https://hub.example:19999', token: 't');
    expect(cfg.uri('/api/v1/info').toString(),
        'https://hub.example:19999/api/v1/info');
    final ws = cfg.wsUri('/api/v1/live', {'chart': 'system.cpu'});
    expect(ws.scheme, 'wss');
    expect(ws.queryParameters['chart'], 'system.cpu');
    expect(cfg.headers['Authorization'], 'Bearer t');
  });

  test('ChartData parses rows and dimension ids', () {
    final d = ChartData.fromJson({
      'dimension_ids': ['user', 'system'],
      'result': {
        'data': [
          [1700000000, 1.5, null],
          [1700000001, 2.0, 3.0],
        ]
      },
    });
    expect(d.dimensionIds, ['user', 'system']);
    expect(d.rows.length, 2);
    expect(d.rows[0][2], isNull);
    expect(d.rows[1][1], 2.0);
  });

  test('Node selector uses local for the hub itself', () {
    final local = Node.fromJson({'id': 'x', 'hostname': 'h', 'local': true});
    final remote = Node.fromJson({'id': 'abc', 'hostname': 'r'});
    expect(local.selector, 'local');
    expect(remote.selector, 'abc');
  });

  test('FunctionResult parses table rows', () {
    final r = FunctionResult.fromJson({
      'function': 'processes',
      'time': 1700000000,
      'result': {
        'columns': ['pid', 'name', 'cpu'],
        'total': 1,
        'rows': [
          {'pid': 1, 'name': 'init', 'cpu': 0.5}
        ],
      },
    });
    expect(r.name, 'processes');
    expect(r.table, isNotNull);
    expect(r.table!.columns, ['pid', 'name', 'cpu']);
    expect(r.table!.rows.first['name'], 'init');
    expect(r.raw, isNull);
  });

  test('FunctionInfo fromJson', () {
    final f = FunctionInfo.fromJson({'name': 'logs', 'help': 'h', 'timeout': 8});
    expect(f.name, 'logs');
    expect(f.timeout, 8);
  });

  test('appVersion default matches pubspec placeholder', () {
    expect(appVersion, isNotEmpty);
  });
}
