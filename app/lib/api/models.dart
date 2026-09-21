/// JSON models for the monitord `/api/v1` surface used by the app.
library;

class ServerInfo {
  ServerInfo({
    required this.mode,
    required this.version,
    required this.hostname,
    required this.chartsCount,
    required this.alarms,
    this.user,
  });

  final String mode;
  final String version;
  final String hostname;
  final int chartsCount;
  final AlarmCounts alarms;
  final String? user;

  bool get isHub => mode == 'hub';

  factory ServerInfo.fromJson(Map<String, dynamic> j) {
    final host = (j['host'] as Map<String, dynamic>?) ?? const {};
    final user = j['user'] as Map<String, dynamic>?;
    return ServerInfo(
      mode: (j['mode'] as String?) ?? 'agent',
      version: (j['version'] as String?) ?? '',
      hostname: (host['hostname'] as String?) ?? '',
      chartsCount: (j['charts_count'] as num?)?.toInt() ?? 0,
      alarms: AlarmCounts.fromJson(j['alarms'] as Map<String, dynamic>?),
      user: user?['name'] as String?,
    );
  }
}

class AlarmCounts {
  const AlarmCounts({this.normal = 0, this.warning = 0, this.critical = 0});

  final int normal;
  final int warning;
  final int critical;

  factory AlarmCounts.fromJson(Map<String, dynamic>? j) {
    if (j == null) return const AlarmCounts();
    return AlarmCounts(
      normal: (j['normal'] as num?)?.toInt() ?? 0,
      warning: (j['warning'] as num?)?.toInt() ?? 0,
      critical: (j['critical'] as num?)?.toInt() ?? 0,
    );
  }
}

class Node {
  Node({
    required this.id,
    required this.hostname,
    required this.os,
    required this.arch,
    required this.status,
    required this.local,
    required this.chartsCount,
    required this.alarms,
    required this.lastSeen,
  });

  /// Empty for the local host; use [selector] as the `node=` query value.
  final String id;
  final String hostname;
  final String os;
  final String arch;
  final String status;
  final bool local;
  final int chartsCount;
  final AlarmCounts alarms;
  final int lastSeen;

  String get selector => local ? 'local' : id;

  factory Node.fromJson(Map<String, dynamic> j) => Node(
        id: (j['id'] as String?) ?? '',
        hostname: (j['hostname'] as String?) ?? '',
        os: (j['os'] as String?) ?? '',
        arch: (j['arch'] as String?) ?? '',
        status: (j['status'] as String?) ?? 'offline',
        local: (j['local'] as bool?) ?? false,
        chartsCount: (j['charts_count'] as num?)?.toInt() ?? 0,
        alarms: AlarmCounts.fromJson(j['alarms'] as Map<String, dynamic>?),
        lastSeen: (j['last_seen'] as num?)?.toInt() ?? 0,
      );
}

class Dimension {
  const Dimension({required this.id, required this.name, this.hidden = false});

  final String id;
  final String name;
  final bool hidden;

  factory Dimension.fromJson(Map<String, dynamic> j) => Dimension(
        id: (j['id'] as String?) ?? '',
        name: (j['name'] as String?) ?? (j['id'] as String?) ?? '',
        hidden: (j['hidden'] as bool?) ?? false,
      );
}

class Chart {
  Chart({
    required this.id,
    required this.title,
    required this.units,
    required this.family,
    required this.context,
    required this.type,
    required this.priority,
    required this.updateEvery,
    required this.dimensions,
  });

  final String id;
  final String title;
  final String units;
  final String family;
  final String context;
  final String type;
  final int priority;
  final int updateEvery;
  final List<Dimension> dimensions;

  factory Chart.fromJson(Map<String, dynamic> j) => Chart(
        id: (j['id'] as String?) ?? '',
        title: (j['title'] as String?) ?? (j['id'] as String?) ?? '',
        units: (j['units'] as String?) ?? '',
        family: (j['family'] as String?) ?? '',
        context: (j['context'] as String?) ?? '',
        type: (j['chart_type'] as String?) ?? 'line',
        priority: (j['priority'] as num?)?.toInt() ?? 0,
        updateEvery: (j['update_every'] as num?)?.toInt() ?? 1,
        dimensions: ((j['dimensions'] as List<dynamic>?) ?? const [])
            .map((d) => Dimension.fromJson(d as Map<String, dynamic>))
            .toList(),
      );
}

/// Result of `/api/v1/data`: rows are `[t, v1, v2, ...]` in dimension order.
class ChartData {
  ChartData({required this.dimensionIds, required this.rows});

  final List<String> dimensionIds;
  final List<List<double?>> rows;

  factory ChartData.fromJson(Map<String, dynamic> j) {
    final result = (j['result'] as Map<String, dynamic>?) ?? const {};
    final data = (result['data'] as List<dynamic>?) ?? const [];
    return ChartData(
      dimensionIds: ((j['dimension_ids'] as List<dynamic>?) ?? const [])
          .map((e) => e as String)
          .toList(),
      rows: data
          .map((r) => (r as List<dynamic>)
              .map((v) => v == null ? null : (v as num).toDouble())
              .toList())
          .toList(),
    );
  }
}

class Alarm {
  Alarm({
    required this.id,
    required this.name,
    required this.chart,
    required this.status,
    required this.value,
    required this.units,
    required this.info,
    required this.lastStatusChange,
  });

  final int id;
  final String name;
  final String chart;
  final String status;
  final double value;
  final String units;
  final String info;
  final int lastStatusChange;

  factory Alarm.fromJson(Map<String, dynamic> j) => Alarm(
        id: (j['id'] as num?)?.toInt() ?? 0,
        name: (j['name'] as String?) ?? '',
        chart: (j['chart'] as String?) ?? '',
        status: (j['status'] as String?) ?? 'UNINITIALIZED',
        value: (j['value'] as num?)?.toDouble() ?? double.nan,
        units: (j['units'] as String?) ?? '',
        info: (j['info'] as String?) ?? '',
        lastStatusChange: (j['last_status_change'] as num?)?.toInt() ?? 0,
      );
}

class AlarmLogEntry {
  AlarmLogEntry({
    required this.uniqueId,
    required this.when,
    required this.name,
    required this.chart,
    required this.status,
    required this.oldStatus,
    required this.value,
    required this.units,
  });

  final int uniqueId;
  final int when;
  final String name;
  final String chart;
  final String status;
  final String oldStatus;
  final double value;
  final String units;

  factory AlarmLogEntry.fromJson(Map<String, dynamic> j) => AlarmLogEntry(
        uniqueId: (j['unique_id'] as num?)?.toInt() ?? 0,
        when: (j['when'] as num?)?.toInt() ?? 0,
        name: (j['name'] as String?) ?? '',
        chart: (j['chart'] as String?) ?? '',
        status: (j['status'] as String?) ?? '',
        oldStatus: (j['old_status'] as String?) ?? '',
        value: (j['value'] as num?)?.toDouble() ?? double.nan,
        units: (j['units'] as String?) ?? '',
      );
}

/// One `/api/v1/live` data frame: `{"chart":..,"t":..,"v":{dim:value}}`.
class LiveSample {
  LiveSample({required this.chart, required this.t, required this.values});

  final String chart;
  final int t;
  final Map<String, double> values;

  static LiveSample? tryParse(Map<String, dynamic> j) {
    final chart = j['chart'];
    final v = j['v'];
    if (chart is! String || v is! Map<String, dynamic>) return null;
    return LiveSample(
      chart: chart,
      t: (j['t'] as num?)?.toInt() ?? 0,
      values: v.map((k, x) => MapEntry(k, (x as num).toDouble())),
    );
  }
}
