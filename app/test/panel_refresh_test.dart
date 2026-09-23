import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:monitor_app/api/client.dart';
import 'package:monitor_app/api/models.dart';
import 'package:monitor_app/screens/home_screen.dart';
import 'package:monitor_app/state/app_state.dart';
import 'package:monitor_app/widgets/alarms_view.dart';
import 'package:monitor_app/widgets/functions_view.dart';

FunctionResult _result(String name, String value) => FunctionResult(
  name: name,
  time: 0,
  table: FunctionTable(
    columns: ['message'],
    rows: [
      {'message': value},
    ],
    total: 1,
  ),
);

class _FunctionRequest {
  _FunctionRequest(this.name, this.node, this.args);
  final String name;
  final String? node;
  final Map<String, String> args;
  final result = Completer<FunctionResult>();
}

class _Client extends ApiClient {
  _Client() : super(const ServerConfig(baseUrl: 'http://test'));
  bool deferAlarms = false;
  bool deferFunctions = false;
  bool failCatalog = false;
  bool failAlarms = false;
  int catalogCalls = 0;
  int logCalls = 0;
  List<FunctionInfo> catalog = const [
    FunctionInfo(name: 'processes'),
    FunctionInfo(name: 'logs'),
  ];
  final alarmsPending = <Completer<List<Alarm>>>[];
  final functionRequests = <_FunctionRequest>[];

  @override
  Future<List<Chart>> charts({String? node}) async => [];

  @override
  Future<List<Alarm>> alarms({String? node, bool all = false}) {
    final request = Completer<List<Alarm>>();
    alarmsPending.add(request);
    if (!deferAlarms) {
      if (failAlarms) {
        request.completeError(Exception('alarm service unavailable'));
      } else {
        request.complete([]);
      }
    }
    return request.future;
  }

  @override
  Future<List<AlarmLogEntry>> alarmLog({String? node}) async {
    logCalls++;
    return [];
  }

  @override
  Future<List<FunctionInfo>> functions({String? node}) async {
    catalogCalls++;
    if (failCatalog) throw Exception('catalog unavailable');
    return catalog;
  }

  @override
  Future<FunctionResult> function(
    String name, {
    String? node,
    Map<String, String> args = const {},
  }) {
    final request = _FunctionRequest(name, node, Map.of(args));
    functionRequests.add(request);
    if (!deferFunctions) request.result.complete(_result(name, '$name fresh'));
    return request.result.future;
  }
}

class _State extends AppState {
  _State(this.api) {
    info = ServerInfo.fromJson({
      'mode': 'agent',
      'host': {'hostname': 'test'},
    });
  }
  final ApiClient api;
  @override
  ApiClient get client => api;
}

Widget _alarms(_Client client, {String? node, bool active = true}) =>
    MaterialApp(
      home: Scaffold(
        body: AlarmsView(client: client, node: node, active: active),
      ),
    );
Widget _functions(_Client client, {String? node, bool active = true}) =>
    MaterialApp(
      home: Scaffold(
        body: FunctionsView(client: client, node: node, active: active),
      ),
    );
Future<void> _flush(WidgetTester tester) async {
  await tester.pump();
  await tester.pump(const Duration(milliseconds: 350));
  await tester.pump();
}

void main() {
  testWidgets(
    'home loads only the selected tab and pauses the previous panel',
    (tester) async {
      final client = _Client();
      final state = _State(client);
      addTearDown(client.close);
      addTearDown(state.dispose);
      await tester.pumpWidget(MaterialApp(home: HomeScreen(state: state)));
      await _flush(tester);
      expect(client.alarmsPending, isEmpty);
      expect(client.catalogCalls, 0);
      await tester.tap(find.text('Functions'));
      await _flush(tester);
      expect(client.functionRequests.length, 1);
      await tester.tap(find.text('Alarms'));
      await _flush(tester);
      expect(client.alarmsPending.length, 1);
      await tester.pump(const Duration(seconds: 20));
      await _flush(tester);
      expect(client.functionRequests.length, 1);
      expect(client.alarmsPending.length, 3);
      await tester.pumpWidget(const SizedBox());
    },
  );

  testWidgets('slow alarm refreshes do not overlap and hiding pauses polling', (
    tester,
  ) async {
    final client = _Client()..deferAlarms = true;
    addTearDown(client.close);
    await tester.pumpWidget(_alarms(client));
    await tester.pump(const Duration(seconds: 40));
    expect(client.alarmsPending.length, 1);
    expect(client.logCalls, 1);
    client.alarmsPending.single.complete([]);
    await _flush(tester);
    await tester.pumpWidget(_alarms(client, active: false));
    await tester.pump(const Duration(seconds: 40));
    expect(client.alarmsPending.length, 1);
    await tester.pumpWidget(_alarms(client, active: true));
    await _flush(tester);
    expect(client.alarmsPending.length, 2);
    client.alarmsPending.last.complete([]);
    await _flush(tester);
    await tester.pumpWidget(const SizedBox());
  });

  testWidgets('failed alarm loading does not report a healthy empty state', (
    tester,
  ) async {
    final client = _Client()..failAlarms = true;
    addTearDown(client.close);
    await tester.pumpWidget(_alarms(client));
    await _flush(tester);
    expect(find.text('Alarm status unavailable'), findsOneWidget);
    expect(find.text('No raised alarms'), findsNothing);
    client.failAlarms = false;
    await tester.pump(const Duration(seconds: 10));
    await _flush(tester);
    expect(find.text('No raised alarms'), findsOneWidget);
    client.failAlarms = true;
    await tester.pump(const Duration(seconds: 10));
    await _flush(tester);
    expect(find.textContaining('last successful snapshot'), findsOneWidget);
    await tester.pumpWidget(const SizedBox());
  });

  testWidgets('an old node alarm error cannot replace a new node snapshot', (
    tester,
  ) async {
    final client = _Client()..deferAlarms = true;
    addTearDown(client.close);
    await tester.pumpWidget(_alarms(client, node: 'old'));
    await _flush(tester);
    await tester.pumpWidget(_alarms(client, node: 'new'));
    await _flush(tester);
    client.alarmsPending.last.complete([]);
    await _flush(tester);
    client.alarmsPending.first.completeError(Exception('old node error'));
    await _flush(tester);
    expect(find.text('No raised alarms'), findsOneWidget);
    expect(find.textContaining('old node error'), findsNothing);
    await tester.pumpWidget(const SizedBox());
  });

  testWidgets('function requests wait for completion before polling again', (
    tester,
  ) async {
    final client = _Client()..deferFunctions = true;
    addTearDown(client.close);
    await tester.pumpWidget(_functions(client));
    await _flush(tester);
    await tester.pump(const Duration(seconds: 20));
    expect(client.functionRequests.length, 1);
    client.functionRequests.single.result.complete(
      _result('processes', 'fresh'),
    );
    await _flush(tester);
    await tester.pump(const Duration(seconds: 2));
    await _flush(tester);
    expect(client.functionRequests.length, 2);
    client.functionRequests.last.result.complete(
      _result('processes', 'updated'),
    );
    await _flush(tester);
    await tester.pumpWidget(const SizedBox());
  });

  testWidgets(
    'function selection discards late results from the previous function',
    (tester) async {
      final client = _Client()..deferFunctions = true;
      addTearDown(client.close);
      await tester.pumpWidget(_functions(client));
      await _flush(tester);
      tester
          .widget<DropdownButton<String>>(find.byType(DropdownButton<String>))
          .onChanged!('logs');
      await _flush(tester);
      client.functionRequests.last.result.complete(
        _result('logs', 'logs fresh'),
      );
      await _flush(tester);
      client.functionRequests.first.result.complete(
        _result('processes', 'processes stale'),
      );
      await _flush(tester);
      expect(find.text('logs fresh'), findsOneWidget);
      expect(find.text('processes stale'), findsNothing);
      await tester.pumpWidget(const SizedBox());
    },
  );

  testWidgets(
    'server filtering debounces input and rejects an obsolete response',
    (tester) async {
      final client = _Client()
        ..deferFunctions = true
        ..catalog = const [FunctionInfo(name: 'logs')];
      addTearDown(client.close);
      await tester.pumpWidget(_functions(client));
      await _flush(tester);
      await tester.enterText(find.byType(TextField), 'a');
      await tester.pump(const Duration(milliseconds: 100));
      await tester.enterText(find.byType(TextField), 'ab');
      await tester.pump(const Duration(milliseconds: 100));
      await tester.enterText(find.byType(TextField), 'abc');
      expect(client.functionRequests.length, 1);
      await _flush(tester);
      expect(client.functionRequests.length, 2);
      expect(client.functionRequests.last.args['query'], 'abc');
      client.functionRequests.last.result.complete(
        _result('logs', 'abc fresh'),
      );
      await _flush(tester);
      client.functionRequests.first.result.complete(
        _result('logs', 'abc stale'),
      );
      await _flush(tester);
      expect(find.text('abc fresh'), findsOneWidget);
      expect(find.text('abc stale'), findsNothing);
      await tester.pumpWidget(const SizedBox());
    },
  );

  testWidgets('catalog failures and empty lists retry automatically', (
    tester,
  ) async {
    final client = _Client()..failCatalog = true;
    addTearDown(client.close);
    await tester.pumpWidget(_functions(client));
    await _flush(tester);
    expect(find.textContaining('catalog unavailable'), findsOneWidget);
    client.failCatalog = false;
    client.catalog = [];
    await tester.pump(const Duration(seconds: 2));
    await _flush(tester);
    expect(find.text('No functions on this node'), findsOneWidget);
    client.catalog = const [FunctionInfo(name: 'logs')];
    await tester.pump(const Duration(seconds: 2));
    await _flush(tester);
    expect(find.text('logs fresh'), findsOneWidget);
    await tester.pumpWidget(const SizedBox());
  });

  testWidgets('hidden functions pause and old node responses stay discarded', (
    tester,
  ) async {
    final client = _Client()..deferFunctions = true;
    addTearDown(client.close);
    await tester.pumpWidget(_functions(client, node: 'old'));
    await _flush(tester);
    await tester.pumpWidget(_functions(client, node: 'old', active: false));
    await tester.pump(const Duration(seconds: 20));
    expect(client.functionRequests.length, 1);
    await tester.pumpWidget(_functions(client, node: 'new'));
    await _flush(tester);
    expect(client.functionRequests.last.node, 'new');
    client.functionRequests.last.result.complete(
      _result('processes', 'new node'),
    );
    await _flush(tester);
    client.functionRequests.first.result.complete(
      _result('processes', 'old node'),
    );
    await _flush(tester);
    expect(find.text('new node'), findsOneWidget);
    expect(find.text('old node'), findsNothing);
    await tester.pumpWidget(const SizedBox());
  });
}
