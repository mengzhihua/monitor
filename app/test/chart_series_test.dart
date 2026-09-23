import 'package:flutter_test/flutter_test.dart';
import 'package:fl_chart/fl_chart.dart';
import 'package:monitor_app/api/models.dart';
import 'package:monitor_app/state/chart_series.dart';

void main() {
  final chart = Chart.fromJson({
    'id': 'test',
    'title': 'Test',
    'dimensions': [
      {'id': 'value', 'name': 'Value'},
    ],
  });
  ChartData history(
    List<List<double?>> rows, {
    List<String> dimensions = const ['value'],
  }) => ChartData(dimensionIds: dimensions, rows: rows);

  test('history preserves nulls and rejects non-finite readings', () {
    final series = ChartSeries(chart, 300);
    series.reset(
      history([
        [100, 4],
        [101, null],
        [102, 6],
        [103, double.infinity],
      ]),
      300,
    );
    expect(series.points['value'], [
      const FlSpot(100, 4),
      FlSpot.nullSpot,
      const FlSpot(102, 6),
      FlSpot.nullSpot,
    ]);
    series.dispose();
  });

  test(
    'missing live dimensions and a missed collection leave explicit gaps',
    () {
      final series = ChartSeries(chart, 300);
      series.reset(
        history([
          [100, 1],
        ]),
        300,
      );
      series.add(101, {'other': 4});
      series.add(103, {'value': 3});
      expect(series.points['value'], [
        const FlSpot(100, 1),
        FlSpot.nullSpot,
        FlSpot.nullSpot,
        const FlSpot(103, 3),
      ]);
      expect(series.points['other'], [
        FlSpot.nullSpot,
        const FlSpot(101, 4),
        FlSpot.nullSpot,
        FlSpot.nullSpot,
      ]);
      expect(series.add(102, {'value': 99}), isFalse);
      series.dispose();
    },
  );

  test(
    'history refresh keeps only newer live samples without reviving null history',
    () {
      final series = ChartSeries(chart, 300);
      series.reset(
        history([
          [99, 1],
          [100, 2],
        ]),
        300,
      );
      series.add(101, {'value': 3});
      series.add(102, {'value': 4});
      series.reset(
        history([
          [100, 5],
          [101, null],
        ]),
        300,
      );
      expect(series.points['value'], [
        const FlSpot(100, 5),
        FlSpot.nullSpot,
        const FlSpot(102, 4),
      ]);
      // A second refresh cannot duplicate the retained sample.
      series.reset(
        history([
          [101, null],
        ]),
        300,
      );
      expect(series.points['value'], [FlSpot.nullSpot, const FlSpot(102, 4)]);
      series.reset(
        history([
          [102, 7],
        ]),
        300,
      );
      expect(series.points['value'], [const FlSpot(102, 7)]);
      series.dispose();
    },
  );

  test(
    'different windows and invalidated definitions discard the old live tail',
    () {
      final series = ChartSeries(chart, 300);
      series.reset(
        history([
          [100, 1],
        ]),
        300,
      );
      series.add(101, {'value': 8});
      series.reset(
        history([
          [100, 2],
        ]),
        3600,
      );
      expect(series.points['value'], [const FlSpot(100, 2)]);
      series.reset(
        history([
          [100, 3],
        ]),
        300,
      );
      series.add(101, {'value': 9});
      series.loaded = false;
      series.reset(
        history([
          [100, 4],
        ]),
        300,
      );
      expect(series.points['value'], [const FlSpot(100, 4)]);
      series.dispose();
    },
  );

  test(
    'idle dimensions expire with the common timeline and remain aligned',
    () {
      final series = ChartSeries(chart, 60);
      series.reset(
        history([
          [100, 42],
        ]),
        60,
      );
      for (var time = 101; time <= 500; time++) {
        series.add(time, {'other': time.toDouble()});
      }
      expect(series.points['value']!.every((point) => point.isNull()), isTrue);
      expect(series.points['value']!.length, series.points['other']!.length);
      expect(series.points['other']!.first.x, 440);
      series.dispose();
    },
  );

  test('slow collectors stay continuous at their declared cadence', () {
    final slow = Chart.fromJson({
      'id': 'slow',
      'update_every': 5,
      'dimensions': [
        {'id': 'value'},
      ],
    });
    final series = ChartSeries(slow, 300);
    series.reset(
      history([
        [100, 1],
      ]),
      300,
    );
    series.add(105, {'value': 2});
    series.add(115, {'value': 3});
    expect(series.points['value'], [
      const FlSpot(100, 1),
      const FlSpot(105, 2),
      FlSpot.nullSpot,
      const FlSpot(115, 3),
    ]);
    series.dispose();
  });
  test('short-window live buffers stay bounded and ignore duplicates', () {
    final series = ChartSeries(chart, 1200);
    for (var i = 1; i <= 10000; i++) {
      series.add(i, {'value': i.toDouble()});
    }
    expect(series.points['value']!.length, lessThanOrEqualTo(1200));
    series.add(9999, {'value': -1});
    expect(series.points['value']!.last.y, 10000);
    series.dispose();
  });
  test(
    'long windows retain aggregated history without accumulating live points',
    () {
      final series = ChartSeries(chart, 86400);
      series.reset(
        ChartData.fromJson({
          'dimension_ids': ['value'],
          'result': {
            'data': [
              [1000, 2],
              [2000, 3],
            ],
          },
        }),
        86400,
      );
      for (var i = 2001; i < 10000; i++) {
        series.add(i, {'value': 1});
      }
      expect(series.points['value']!.length, 2);
      expect(series.points['value']!.last.y, 3);
      series.dispose();
    },
  );
}
