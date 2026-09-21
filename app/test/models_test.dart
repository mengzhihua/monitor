import 'package:flutter_test/flutter_test.dart';
import 'package:monitor_app/api/client.dart';
import 'package:monitor_app/api/models.dart';

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
}
