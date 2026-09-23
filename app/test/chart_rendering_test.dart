import 'package:fl_chart/fl_chart.dart';
import 'package:flutter/material.dart';
import 'package:flutter/rendering.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:monitor_app/api/models.dart';
import 'package:monitor_app/state/chart_series.dart';
import 'package:monitor_app/widgets/charts_view.dart';

ChartSeries _series(String id, int time, {int dimensions = 20}) {
  final chart = Chart.fromJson({
    'id': id,
    'title': id,
    'dimensions': [
      for (var d = 0; d < dimensions; d++) {'id': 'd$d', 'name': 'D$d'},
    ],
  });
  return ChartSeries(chart, 1200)..reset(
    ChartData(
      dimensionIds: [for (var d = 0; d < dimensions; d++) 'd$d'],
      rows: [
        for (var i = 0; i < 1200; i++)
          [
            (time - 1199 + i).toDouble(),
            for (var d = 0; d < dimensions; d++) 10.0 + d + i % 5,
          ],
      ],
    ),
    1200,
  );
}

Widget _cards(List<ChartSeries> series) => MaterialApp(
  home: Scaffold(
    body: ListView(
      children: [
        Row(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            for (final item in series)
              Expanded(
                child: ChartCard(
                  chart: item.chart,
                  series: item,
                  onVisibilityChanged: (_, _, _) {},
                ),
              ),
          ],
        ),
      ],
    ),
  ),
);

void main() {
  testWidgets('a live gap enables its isolated dot until the next reading', (
    tester,
  ) async {
    final now = DateTime.now().millisecondsSinceEpoch ~/ 1000;
    final series = _series('gap', now, dimensions: 1);
    await tester.pumpWidget(_cards([series]));
    LineChartBarData bar() => tester
        .widget<LineChart>(find.byType(LineChart))
        .data
        .lineBarsData
        .single;
    expect(bar().dotData.show, isFalse);
    series.add(now + 3, {'d0': 99});
    series.notify();
    await tester.pump();
    final isolated = bar();
    expect(isolated.spots[isolated.spots.length - 2].isNull(), isTrue);
    expect(isolated.dotData.show, isTrue);
    expect(
      isolated.dotData.checkToShowDot(isolated.spots.last, isolated),
      isTrue,
    );
    series.add(now + 4, {'d0': 98});
    series.notify();
    await tester.pump();
    expect(bar().dotData.show, isFalse);
    expect(bar().spots.last.y, 98);
    await tester.pumpWidget(const SizedBox());
    series.dispose();
  });

  testWidgets('live samples repaint only their own chart in a wide row', (
    tester,
  ) async {
    tester.view.physicalSize = const Size(1440, 1000);
    tester.view.devicePixelRatio = 1;
    addTearDown(tester.view.resetPhysicalSize);
    addTearDown(tester.view.resetDevicePixelRatio);
    final now = DateTime.now().millisecondsSinceEpoch ~/ 1000;
    final series = [for (var i = 0; i < 3; i++) _series('chart$i', now)];
    await tester.pumpWidget(_cards(series));
    await tester.pumpAndSettle();
    final plots = tester.allRenderObjects
        .where((object) => object.runtimeType.toString() == 'RenderLineChart')
        .toSet()
        .toList();
    expect(plots.length, 3);
    final elements = tester.elementList(find.byType(LineChart)).toList();
    final paints = [0, 0, 0];
    final builds = [0, 0, 0];
    final previousPaint = debugOnProfilePaint;
    final previousBuild = debugOnRebuildDirtyWidget;
    debugOnProfilePaint = (object) {
      previousPaint?.call(object);
      final index = plots.indexOf(object);
      if (index >= 0) paints[index]++;
    };
    debugOnRebuildDirtyWidget = (element, builtOnce) {
      previousBuild?.call(element, builtOnce);
      final index = elements.indexOf(element);
      if (index >= 0) builds[index]++;
    };
    addTearDown(() {
      debugOnProfilePaint = previousPaint;
      debugOnRebuildDirtyWidget = previousBuild;
    });
    for (var tick = 1; tick <= 30; tick++) {
      series.first.add(now + tick, {
        for (var d = 0; d < 20; d++) 'd$d': 10.0 + d + tick % 5,
      });
      series.first.notify();
      await tester.pump(const Duration(seconds: 1));
    }
    debugPrint(
      'CHART_RENDERING dimensions=20 points=1200 ticks=30 '
      'paints=$paints builds=$builds',
    );
    expect(builds, [30, 0, 0]);
    expect(paints, [30, 0, 0]);
    expect(series.first.points['d0']!.last.x, now + 30);
    debugOnProfilePaint = previousPaint;
    debugOnRebuildDirtyWidget = previousBuild;
    await tester.pumpWidget(const SizedBox());
    for (final item in series) {
      item.dispose();
    }
  });

  testWidgets(
    'continuous dimensions skip dot painting without losing samples',
    (tester) async {
      final now = DateTime.now().millisecondsSinceEpoch ~/ 1000;
      final series = _series('continuous', now);
      await tester.pumpWidget(_cards([series]));
      final plot = tester.widget<LineChart>(find.byType(LineChart));
      final bars = plot.data.lineBarsData;
      expect(bars.length, 20);
      expect(bars.every((bar) => bar.spots.length == 1200), isTrue);
      var dotChecks = 0;
      final measured = plot.data.copyWith(
        lineBarsData: [
          for (final bar in bars)
            bar.copyWith(
              dotData: FlDotData(
                show: bar.dotData.show,
                checkToShowDot: (spot, line) {
                  dotChecks++;
                  return bar.dotData.checkToShowDot(spot, line);
                },
                getDotPainter: bar.dotData.getDotPainter,
              ),
            ),
        ],
      );
      await tester.pumpWidget(
        MaterialApp(
          home: Scaffold(
            body: Center(
              child: SizedBox(
                width: 480,
                height: 120,
                child: LineChart(measured, duration: Duration.zero),
              ),
            ),
          ),
        ),
      );
      debugPrint('CHART_DOT_CHECKS dimensions=20 points=1200 calls=$dotChecks');
      expect(dotChecks, 0);
      expect(bars.every((bar) => !bar.dotData.show), isTrue);
      await tester.pumpWidget(const SizedBox());
      series.dispose();
    },
  );
}
