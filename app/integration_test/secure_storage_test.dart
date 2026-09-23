import 'dart:io';
import 'package:flutter/foundation.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:integration_test/integration_test.dart';
import 'package:monitor_app/state/credential_store.dart';

void main() {
  IntegrationTestWidgetsFlutterBinding.ensureInitialized();
  testWidgets('native secure store writes, reads and removes an isolated key', (
    tester,
  ) async {
    await tester.pumpWidget(
      const MaterialApp(
        home: Scaffold(body: Text('Secure storage acceptance')),
      ),
    );
    // Widget tests default to Android even when running on a native Mac.
    if (Platform.isMacOS) {
      debugDefaultTargetPlatformOverride = TargetPlatform.macOS;
    }
    final key = 'monitor.acceptance.${DateTime.now().microsecondsSinceEpoch}';
    final store = CredentialStore(storageKey: key);
    const value = 'test-only-credential';
    var wrote = false;
    try {
      await CredentialStore.storage.write(key: key, value: value);
      wrote = true;
      expect(await CredentialStore.storage.read(key: key), value);
      await store.clear();
      expect(await CredentialStore.storage.read(key: key), isNull);
      await store.clear(); // Disconnect/forget is idempotent.
    } finally {
      try {
        if (wrote) await store.clear();
      } finally {
        debugDefaultTargetPlatformOverride = null;
      }
    }
  });
}
