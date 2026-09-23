import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:monitor_app/api/client.dart';
import 'package:monitor_app/api/models.dart';
import 'package:monitor_app/widgets/charts_view.dart';

class _HistoryRequest {
  _HistoryRequest(this.chart, this.node, this.window);
  final String chart;
  final String? node;
  final int window;
  final result = Completer<ChartData>();

  void complete([double value = 1]) => result.complete(
    ChartData(
      dimensionIds: ['value'],
      rows: [
        [DateTime.now().millisecondsSinceEpoch / 1000, value],
      ],
    ),
  );
}

class _Live implements LiveSubscription {
  _Live(List<String> charts) : charts = charts.toSet();
  final controller = StreamController<LiveSample>.broadcast();
  Set<String> charts;
  int updates = 0;
  int closes = 0;

  @override
  Stream<LiveSample> get samples => controller.stream;

  @override
  void setCharts(List<String> charts) {
    this.charts = charts.toSet();
    updates++;
  }

  @override
  Future<void> close() async {
    closes++;
    await controller.close();
  }
}

class _Client extends ApiClient {
  _Client({this.count = 100})
    : super(const ServerConfig(baseUrl: 'http://test'));
  int count;
  bool deferHistory = false;
  int chartCalls = 0;
  final requests = <_HistoryRequest>[];
  final sockets = <_Live>[];

  @override
  Future<List<Chart>> charts({String? node}) async {
    chartCalls++;
    return [
      for (var i = 0; i < count; i++)
        Chart.fromJson({
          'id': 'chart${i.toString().padLeft(3, '0')}',
          'family': 'large-family',
          'title': 'Chart $i',
          'priority': i,
          'dimensions': [
            {'id': 'value', 'name': 'Value'},
          ],
        }),
    ];
  }

  @override
  Future<ChartData> data(
    String chart, {
    String? node,
    int afterSeconds = 300,
    int points = 300,
  }) {
    final request = _HistoryRequest(chart, node, afterSeconds);
    requests.add(request);
    if (!deferHistory) request.complete();
    return request.result.future;
  }

  @override
  LiveSubscription live({required List<String> charts, String? node}) {
    final socket = _Live(charts);
    sockets.add(socket);
    return socket;
  }
}

Widget _view(_Client client, {String? node, bool active = true}) => MaterialApp(
  home: Scaffold(
    body: ChartsView(client: client, node: node, active: active),
  ),
);

Future<void> _settleCharts(WidgetTester tester) async {
  await tester.pump();
  await tester.pump(const Duration(milliseconds: 60));
  await tester.pump(const Duration(milliseconds: 300));
  await tester.pump(const Duration(milliseconds: 60));
  await tester.pump(const Duration(milliseconds: 300));
}

Set<String> _mountedCharts(WidgetTester tester) => tester
    .widgetList<ChartCard>(find.byType(ChartCard, skipOffstage: false))
    .map((card) => card.chart.id)
    .toSet();

void main() {
  testWidgets(
    'failed initial history shows an error and a later refresh recovers',
    (tester) async {
      final client = _Client(count: 1)..deferHistory = true;
      addTearDown(client.close);
      await tester.pumpWidget(_view(client));
      await _settleCharts(tester);
      client.requests.single.result.completeError(
        Exception('history unavailable'),
      );
      await _settleCharts(tester);
      expect(
        find.text('History unavailable. Retrying automatically.'),
        findsOneWidget,
      );
      expect(find.byType(CircularProgressIndicator), findsNothing);
      client.deferHistory = false;
      await tester.pump(const Duration(seconds: 30));
      await _settleCharts(tester);
      final series = tester.widget<ChartCard>(find.byType(ChartCard)).series;
      expect(series.loaded, isTrue);
      expect(series.historyFailed, isFalse);
      expect(
        find.text('History unavailable. Retrying automatically.'),
        findsNothing,
      );
      await tester.pumpWidget(const SizedBox());
    },
  );

  testWidgets(
    'slow and failed refreshes preserve live readings already received',
    (tester) async {
      final client = _Client(count: 1)..deferHistory = true;
      addTearDown(client.close);
      await tester.pumpWidget(_view(client));
      await _settleCharts(tester);
      final time = DateTime.now().millisecondsSinceEpoch ~/ 1000;
      client.requests.single.result.complete(
        ChartData(
          dimensionIds: ['value'],
          rows: [
            [time.toDouble(), 1],
          ],
        ),
      );
      await _settleCharts(tester);
      await tester.pump(const Duration(seconds: 30));
      await _settleCharts(tester);
      client.sockets.single.controller.add(
        LiveSample(chart: 'chart000', t: time + 1, values: {'value': 99}),
      );
      await tester.pump();
      client.requests.last.result.complete(
        ChartData(
          dimensionIds: ['value'],
          rows: [
            [time.toDouble(), 2],
          ],
        ),
      );
      await _settleCharts(tester);
      final series = tester.widget<ChartCard>(find.byType(ChartCard)).series;
      expect(series.points['value']!.last.y, 99);
      await tester.pump(const Duration(seconds: 30));
      await _settleCharts(tester);
      client.requests.last.result.completeError(
        Exception('history unavailable'),
      );
      await _settleCharts(tester);
      expect(series.points['value']!.last.y, 99);
      expect(find.byIcon(Icons.sync_problem), findsOneWidget);
      await tester.pumpWidget(const SizedBox());
    },
  );

  testWidgets(
    'removed charts release their subscription and stop history requests',
    (tester) async {
      final client = _Client(count: 1);
      addTearDown(client.close);
      await tester.pumpWidget(_view(client));
      await _settleCharts(tester);
      final requests = client.requests.length;
      client.count = 0;
      await tester.pump(const Duration(seconds: 30));
      await _settleCharts(tester);
      expect(find.byType(ChartCard, skipOffstage: false), findsNothing);
      expect(client.sockets.single.closes, 1);
      expect(client.requests.length, requests);
      expect(tester.takeException(), isNull);
      await tester.pumpWidget(const SizedBox());
    },
  );

  testWidgets(
    'hidden charts tab pauses polling and resumes with fresh history',
    (tester) async {
      final client = _Client(count: 1);
      addTearDown(client.close);
      await tester.pumpWidget(_view(client));
      await _settleCharts(tester);
      final calls = client.chartCalls;
      final requests = client.requests.length;
      await tester.pumpWidget(_view(client, active: false));
      await tester.pump(const Duration(seconds: 90));
      expect(client.chartCalls, calls);
      expect(client.requests.length, requests);
      expect(client.sockets.single.closes, 1);
      await tester.pumpWidget(_view(client, active: true));
      await _settleCharts(tester);
      expect(client.chartCalls, calls + 1);
      expect(client.requests.length, requests + 1);
      expect(client.sockets.length, 2);
      await tester.pumpWidget(const SizedBox());
    },
  );

  for (final width in [390.0, 1200.0]) {
    testWidgets('lazy rows and subscription reuse at width $width', (
      tester,
    ) async {
      tester.view.physicalSize = Size(width, 800);
      tester.view.devicePixelRatio = 1;
      addTearDown(tester.view.resetPhysicalSize);
      addTearDown(tester.view.resetDevicePixelRatio);
      final client = _Client();
      addTearDown(client.close);
      await tester.pumpWidget(_view(client));
      await _settleCharts(tester);

      final initial = _mountedCharts(tester);
      expect(initial.length, inExclusiveRange(0, 20));
      expect(client.requests.length, lessThan(20));
      expect(
        client.requests.map((r) => r.chart).toSet().containsAll(initial),
        isTrue,
      );
      expect(client.sockets.single.charts, initial);
      final initialRequests = client.requests.length;
      final initialUpdates = client.sockets.single.updates;

      await tester.pump(const Duration(seconds: 30));
      await _settleCharts(tester);
      expect(
        client.sockets.length,
        1,
        reason: 'metadata polling must reuse the socket',
      );
      expect(client.sockets.single.updates, initialUpdates);
      expect(client.requests.length, initialRequests + initial.length);

      await tester.drag(find.byType(ListView), const Offset(0, -1800));
      await _settleCharts(tester);
      expect(_mountedCharts(tester).contains('chart000'), isFalse);
      expect(client.sockets.length, 1);
      expect(client.sockets.single.charts, _mountedCharts(tester));
      expect(client.sockets.single.updates, greaterThan(0));
      final beforeRefresh = client.requests.length;
      await tester.pump(const Duration(seconds: 30));
      await _settleCharts(tester);
      expect(
        client.requests.skip(beforeRefresh).map((r) => r.chart).toSet(),
        _mountedCharts(tester),
      );
      expect(tester.takeException(), isNull);
      await tester.pumpWidget(const SizedBox());
      await tester.pump();
      expect(client.sockets.single.closes, 1);
    });
  }

  testWidgets('filter replaces subscriptions without reopening the socket', (
    tester,
  ) async {
    final client = _Client();
    addTearDown(client.close);
    await tester.pumpWidget(_view(client));
    await _settleCharts(tester);
    await tester.enterText(find.byType(TextField), 'chart099');
    await _settleCharts(tester);
    expect(client.sockets.length, 1);
    expect(client.sockets.single.charts, {'chart099'});
    expect(client.requests.last.chart, 'chart099');
    await tester.enterText(find.byType(TextField), 'no matches');
    await _settleCharts(tester);
    expect(client.sockets.single.closes, 1);
    await tester.pumpWidget(const SizedBox());
  });

  testWidgets('late node responses cannot overwrite the new node', (
    tester,
  ) async {
    final client = _Client(count: 1)..deferHistory = true;
    addTearDown(client.close);
    await tester.pumpWidget(_view(client, node: 'old'));
    await _settleCharts(tester);
    final old = client.requests.single;
    await tester.pumpWidget(_view(client, node: 'new'));
    await _settleCharts(tester);
    final current = client.requests.last;
    expect(current.node, 'new');
    old.complete(11);
    await _settleCharts(tester);
    expect(
      tester.widget<ChartCard>(find.byType(ChartCard)).series.loaded,
      isFalse,
    );
    current.complete(33);
    await _settleCharts(tester);
    final series = tester.widget<ChartCard>(find.byType(ChartCard)).series;
    expect(series.points['value']!.single.y, 33);
    expect(client.requests.length, 2);
    expect(client.sockets.first.closes, 1);
    await tester.pumpWidget(const SizedBox());
  });

  testWidgets('window changes reject old history and pause unused live data', (
    tester,
  ) async {
    final client = _Client(count: 1)..deferHistory = true;
    addTearDown(client.close);
    await tester.pumpWidget(_view(client));
    await _settleCharts(tester);
    tester
        .widget<DropdownButton<int>>(find.byType(DropdownButton<int>))
        .onChanged!(3600);
    await _settleCharts(tester);
    expect(client.sockets.single.closes, 1);
    client.requests.first.complete(11);
    await _settleCharts(tester);
    expect(client.requests.last.window, 3600);
    client.requests.last.complete(22);
    await _settleCharts(tester);
    final series = tester.widget<ChartCard>(find.byType(ChartCard)).series;
    expect(series.window, 3600);
    expect(series.points['value']!.single.y, 22);
    await tester.pumpWidget(const SizedBox());
  });

  testWidgets('socket loss reconnects once and refreshes missing history', (
    tester,
  ) async {
    final client = _Client(count: 1);
    addTearDown(client.close);
    await tester.pumpWidget(_view(client));
    await _settleCharts(tester);
    await client.sockets.single.controller.close();
    await tester.pump();
    await tester.pump(const Duration(seconds: 3));
    await _settleCharts(tester);
    expect(client.sockets.length, 2);
    expect(client.requests.length, 2);
    await tester.pump(const Duration(seconds: 3));
    expect(client.sockets.length, 2);
    await tester.pumpWidget(const SizedBox());
  });
}
