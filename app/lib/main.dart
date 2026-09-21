import 'package:flutter/material.dart';

import 'screens/connect_screen.dart';
import 'screens/home_screen.dart';
import 'state/app_state.dart';

void main() {
  runApp(const MonitorApp());
}

class MonitorApp extends StatefulWidget {
  const MonitorApp({super.key, this.state});

  final AppState? state;

  @override
  State<MonitorApp> createState() => _MonitorAppState();
}

class _MonitorAppState extends State<MonitorApp> {
  late final AppState _state = widget.state ?? AppState();
  late final Future<void> _restore = _state.restore();

  @override
  Widget build(BuildContext context) {
    return MaterialApp(
      title: 'Monitor',
      debugShowCheckedModeBanner: false,
      theme: ThemeData(
        colorSchemeSeed: const Color(0xFF00A86B),
        brightness: Brightness.dark,
        useMaterial3: true,
      ),
      home: FutureBuilder<void>(
        future: _restore,
        builder: (context, snap) {
          if (snap.connectionState != ConnectionState.done) {
            return const Scaffold(body: Center(child: CircularProgressIndicator()));
          }
          return AnimatedBuilder(
            animation: _state,
            builder: (context, _) => _state.connected
                ? HomeScreen(state: _state)
                : ConnectScreen(state: _state),
          );
        },
      ),
    );
  }
}
