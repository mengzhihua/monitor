import 'package:fl_chart/fl_chart.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:monitor_app/api/models.dart';
import 'package:monitor_app/state/chart_series.dart';
import 'package:monitor_app/widgets/charts_view.dart';

void main() {
  final chart = Chart.fromJson({
    'id': 'test',
    'dimensions': [
      {'id': 'value', 'name': 'Value'},
    ],
  });
  Widget card(ChartSeries series) => MaterialApp(
    home: Scaffold(
      body: ChartCard(
        chart: chart,
        series: series,
        onVisibilityChanged: (_, _, _) {},
      ),
    ),
  );

  testWidgets(
    'isolated readings are drawn and a missing latest value stays unknown',
    (tester) async {
      final now = DateTime.now().millisecondsSinceEpoch / 1000;
      final series = ChartSeries(chart, 300)
        ..reset(
          ChartData(
            dimensionIds: ['value'],
            rows: [
              [now - 3, 4],
              [now - 2, null],
              [now - 1, 8],
              [now, null],
            ],
          ),
          300,
        );
      await tester.pumpWidget(card(series));
      final plot = tester.widget<LineChart>(find.byType(LineChart));
      final bar = plot.data.lineBarsData.single;
      expect(bar.spots[1].isNull(), isTrue);
      expect(bar.dotData.checkToShowDot(bar.spots[0], bar), isTrue);
      expect(bar.dotData.checkToShowDot(bar.spots[2], bar), isTrue);
      expect(find.text('-'), findsOneWidget);
      expect(tester.takeException(), isNull);
      await tester.pumpWidget(const SizedBox());
      series.dispose();
    },
  );

  testWidgets(
    'one finite reading remains visible but all-null data has no line',
    (tester) async {
      final now = DateTime.now().millisecondsSinceEpoch / 1000;
      final series = ChartSeries(chart, 300)
        ..reset(
          ChartData(
            dimensionIds: ['value'],
            rows: [
              [now, 4],
            ],
          ),
          300,
        );
      await tester.pumpWidget(card(series));
      var plot = tester.widget<LineChart>(find.byType(LineChart));
      final bar = plot.data.lineBarsData.single;
      expect(bar.dotData.checkToShowDot(bar.spots.single, bar), isTrue);
      series.reset(
        ChartData(
          dimensionIds: ['value'],
          rows: [
            [now, null],
          ],
        ),
        300,
      );
      await tester.pump();
      plot = tester.widget<LineChart>(find.byType(LineChart));
      expect(plot.data.lineBarsData, isEmpty);
      expect(tester.takeException(), isNull);
      await tester.pumpWidget(const SizedBox());
      series.dispose();
    },
  );
}
