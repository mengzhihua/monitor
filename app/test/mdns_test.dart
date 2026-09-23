import 'dart:typed_data';

import 'package:flutter_test/flutter_test.dart';
import 'package:monitor_app/discover/mdns.dart';

void main() {
  test('parse SRV and A answers', () {
    final pkt = <int>[
      0, 0, 0x84, 0, 0, 0, 0, 2, 0, 0, 0, 0,
      // SRV monitor.local
      7, ... 'monitor'.codeUnits, 5, ... 'local'.codeUnits, 0,
      0, 33, 0, 1, 0, 0, 0, 120, 0, 6,
      0, 0, 0, 0, 0x4e, 0x1f,
      // A
      7, ... 'monitor'.codeUnits, 5, ... 'local'.codeUnits, 0,
      0, 1, 0, 1, 0, 0, 0, 120, 0, 4,
      10, 1, 2, 3,
    ];
    expect(parseMonitorResponse(Uint8List.fromList(pkt)), 'http://10.1.2.3:19999');
  });
}
