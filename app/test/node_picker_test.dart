import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:monitor_app/api/models.dart';
import 'package:monitor_app/state/app_state.dart';
import 'package:monitor_app/widgets/node_picker.dart';

class _State extends AppState {
  final response = Completer<List<Node>>();

  @override
  Future<void> refreshNodes() async {
    nodes = await response.future;
    notifyListeners();
  }

  @override
  Future<void> selectNode(String selector) async {
    selectedNode = selector;
  }
}

void main() {
  for (final width in [320.0, 1000.0]) {
    testWidgets(
      'refreshed nodes can be searched and selected at width $width',
      (tester) async {
        tester.view.devicePixelRatio = 1;
        tester.view.physicalSize = Size(width, 640);
        addTearDown(tester.view.resetDevicePixelRatio);
        addTearDown(tester.view.resetPhysicalSize);
        final state = _State();
        addTearDown(state.dispose);
        await tester.pumpWidget(
          MaterialApp(
            home: Scaffold(body: NodePicker(state: state)),
          ),
        );
        await tester.tap(find.byTooltip('Select node'));
        await tester.pump();
        await tester.pump(const Duration(milliseconds: 300));
        expect(find.byType(CircularProgressIndicator), findsOneWidget);
        await tester.enterText(find.byType(TextField), ' HOST-B ');
        state.response.complete([
          for (final id in ['host-a', 'host-b'])
            Node.fromJson({
              'id': id,
              'hostname': 'Same hostname',
              'status': 'live',
            }),
        ]);
        await tester.pumpAndSettle();
        expect(find.text('Same hostname'), findsOneWidget);
        await tester.enterText(find.byType(TextField), 'unknown');
        await tester.pump();
        expect(find.text('No nodes match your search.'), findsOneWidget);
        await tester.enterText(find.byType(TextField), 'same HOST');
        await tester.pump();
        expect(find.text('Same hostname'), findsNWidgets(2));
        await tester.tap(find.textContaining('host-b · live'));
        await tester.pumpAndSettle();
        expect(state.selectedNode, 'host-b');
        expect(find.byType(Dialog), findsNothing);
      },
    );
  }
}
