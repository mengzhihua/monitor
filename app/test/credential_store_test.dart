import 'package:flutter_test/flutter_test.dart';
import 'package:flutter_secure_storage/flutter_secure_storage.dart';
import 'package:shared_preferences/shared_preferences.dart';
import 'package:monitor_app/api/client.dart';
import 'package:monitor_app/state/credential_store.dart';

void main() {
  TestWidgetsFlutterBinding.ensureInitialized();
  setUp(() {
    SharedPreferences.setMockInitialValues({'server.token': 'legacy-secret'});
    FlutterSecureStorage.setMockInitialValues({});
  });
  test('remembers securely and removes legacy plaintext token', () async {
    final store = CredentialStore();
    await store.save(
      const ServerConfig(baseUrl: 'https://a', token: 'secret'),
      remember: true,
    );
    expect(await store.load('https://a'), 'secret');
    expect(
      (await SharedPreferences.getInstance()).containsKey('server.token'),
      false,
    );
  });
  test('another endpoint never receives the saved credential', () async {
    final store = CredentialStore();
    await store.save(
      const ServerConfig(baseUrl: 'https://a', token: 'secret'),
      remember: true,
    );
    expect(await store.load('https://b'), '');
    expect(await store.load('http://a'), '');
  });
  test('session-only and disconnect erase saved credentials', () async {
    final store = CredentialStore();
    const config = ServerConfig(baseUrl: 'https://a', token: 'secret');
    await store.save(config, remember: true);
    await store.save(config, remember: false);
    expect(await store.load('https://a'), '');
    await store.save(config, remember: true);
    await store.clear();
    expect(await store.load('https://a'), '');
  });
}
