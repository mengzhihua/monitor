import 'dart:io';
import 'dart:typed_data';

/// DNS-SD lookup for `_monitor._tcp.local`. Returns `http://ip:port` or null.
Future<String?> discoverMonitor({Duration timeout = const Duration(seconds: 2)}) async {
  RawDatagramSocket? socket;
  try {
    socket = await RawDatagramSocket.bind(InternetAddress.anyIPv4, 0);
    socket.broadcastEnabled = true;
    final query = mdnsQuery();
    socket.send(query, InternetAddress('224.0.0.251'), 5353);
    final deadline = DateTime.now().add(timeout);
    while (DateTime.now().isBefore(deadline)) {
      final event = await socket.timeout(timeout).first;
      if (event != RawSocketEvent.read) continue;
      final dg = socket.receive();
      if (dg == null) continue;
      final url = parseMonitorResponse(dg.data);
      if (url != null) return url;
    }
  } catch (_) {
    return null;
  } finally {
    socket?.close();
  }
  return null;
}

Uint8List mdnsQuery() {
  final b = BytesBuilder();
  b.add([0, 1, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0]);
  b.add(encodeName('_monitor._tcp.local'));
  b.add([0, 12, 0, 1]);
  return b.toBytes();
}

String? parseMonitorResponse(Uint8List pkt) {
  if (pkt.length < 12) return null;
  final an = (pkt[6] << 8) | pkt[7];
  var off = 12;
  final qd = (pkt[4] << 8) | pkt[5];
  for (var i = 0; i < qd; i++) {
    final next = skipName(pkt, off);
    if (next < 0 || next + 4 > pkt.length) return null;
    off = next + 4;
  }
  var port = 0;
  InternetAddress? ip;
  for (var i = 0; i < an && off + 10 < pkt.length; i++) {
    final next = skipName(pkt, off);
    if (next < 0 || next + 10 > pkt.length) return null;
    final typ = (pkt[next] << 8) | pkt[next + 1];
    final rdlen = (pkt[next + 8] << 8) | pkt[next + 9];
    final data = next + 10;
    if (data + rdlen > pkt.length) return null;
    if (typ == 33 && rdlen >= 6) {
      port = (pkt[data + 4] << 8) | pkt[data + 5];
    } else if (typ == 1 && rdlen >= 4) {
      ip = InternetAddress(
        '${pkt[data]}.${pkt[data + 1]}.${pkt[data + 2]}.${pkt[data + 3]}',
      );
    }
    off = data + rdlen;
  }
  if (port == 0 || ip == null) return null;
  return 'http://${ip.address}:$port';
}

List<int> encodeName(String name) {
  final out = <int>[];
  for (final label in name.split('.')) {
    if (label.isEmpty) continue;
    out.add(label.length);
    out.addAll(label.codeUnits);
  }
  out.add(0);
  return out;
}

int skipName(Uint8List pkt, int off) {
  var jumped = false;
  var next = off;
  for (var steps = 0; steps < 16 && off < pkt.length; steps++) {
    final n = pkt[off];
    if (n == 0) return jumped ? next : off + 1;
    if (n & 0xc0 == 0xc0) {
      if (off + 1 >= pkt.length) return -1;
      if (!jumped) {
        next = off + 2;
        jumped = true;
      }
      off = ((n & 0x3f) << 8) | pkt[off + 1];
      continue;
    }
    if (off + 1 + n > pkt.length) return -1;
    off += 1 + n;
    if (!jumped) next = off;
  }
  return -1;
}
