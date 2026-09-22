/// Release CI passes `--dart-define=APP_VERSION=<tag without v>`.
/// Local `flutter run` falls back to the pubspec version.
const String appVersion =
    String.fromEnvironment('APP_VERSION', defaultValue: '0.1.0');
