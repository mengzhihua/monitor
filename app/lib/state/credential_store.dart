import 'dart:convert';

import 'package:flutter_secure_storage/flutter_secure_storage.dart';
import 'package:shared_preferences/shared_preferences.dart';

import '../api/client.dart';

/// Store the endpoint and its credential together so a changed endpoint can
/// never inherit another server's token. No plaintext fallback is permitted.
class CredentialStore {
  CredentialStore({this.storageKey = key});
  final String storageKey;
  static const key = 'monitor.connection.v1';
  static const storage = FlutterSecureStorage(
    mOptions: MacOsOptions(usesDataProtectionKeychain: false),
  );

  Future<String> load(String url) async {
    final prefs = await SharedPreferences.getInstance();
    await prefs.remove('server.token');
    final value = await storage.read(key: storageKey);
    if (value == null) return '';
    final record = jsonDecode(value) as Map<String, dynamic>;
    return record['url'] == url ? record['token'] as String? ?? '' : '';
  }

  Future<void> save(ServerConfig config, {required bool remember}) async {
    final prefs = await SharedPreferences.getInstance();
    await prefs.remove('server.token');
    if (remember && config.token.isNotEmpty) {
      await storage.write(
        key: storageKey,
        value: jsonEncode({'url': config.baseUrl, 'token': config.token}),
      );
    } else {
      await clear();
    }
  }

  Future<void> clear() async {
    // The Darwin plugin also probes the sync keychain when deleting. On an
    // unsigned Mac, deleting an absent item can report a missing entitlement.
    // Read our exact key first; never hide a failure to delete an existing item.
    if (await storage.read(key: storageKey) != null) {
      await storage.delete(key: storageKey);
    }
  }
}
