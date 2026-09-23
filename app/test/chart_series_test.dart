import 'package:flutter_test/flutter_test.dart';
import 'package:monitor_app/api/models.dart';
import 'package:monitor_app/widgets/charts_view.dart';

void main() {
  final chart = Chart.fromJson({
    'id': 'test',
    'title': 'Test',
    'dimensions': [
      {'id': 'value', 'name': 'Value'},
    ],
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
