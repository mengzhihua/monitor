import 'dart:async';
import 'dart:convert';
import 'dart:io';

import 'package:flutter_test/flutter_test.dart';
import 'package:monitor_app/api/client.dart';

void main() {
  test(
    'live upgrade scopes the first frame and empty updates select no charts',
    () async {
      final server = await HttpServer.bind(InternetAddress.loopbackIPv4, 0);
      final client = ApiClient(
        ServerConfig(baseUrl: 'http://127.0.0.1:${server.port}'),
      );
      final incoming = StreamIterator(server);
      LiveSubscription? live;
      WebSocket? peer;
      try {
        live = client.live(charts: ['cpu', 'ram'], node: 'node-a');
        expect(await incoming.moveNext(), isTrue);
        final request = incoming.current;
        expect(request.uri.queryParameters['charts'], 'cpu,ram');
        expect(request.uri.queryParameters['node'], 'node-a');
        peer = await WebSocketTransformer.upgrade(
          request,
          protocolSelector: (protocols) =>
              protocols.contains('monitor') ? 'monitor' : null,
        );
        final messages = StreamIterator(peer);
        live.setCharts([]);
        expect(await messages.moveNext(), isTrue);
        expect(jsonDecode(messages.current as String), {
          'charts': [' '],
        });
        live.setCharts(['disk']);
        expect(await messages.moveNext(), isTrue);
        expect(jsonDecode(messages.current as String), {
          'charts': ['disk'],
        });
        await messages.cancel();
      } finally {
        await peer?.close();
        await live?.close();
        await incoming.cancel();
        await server.close(force: true);
        client.close();
      }
    },
  );
}
