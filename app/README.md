# Monitor client

Flutter client for the monitord Agent/Hub (macOS, Windows, Linux, Android, iOS). See the repository README.

```bash
flutter pub get
flutter run -d <device>
flutter analyze && flutter test
```

## Linux desktop

The release tarball needs GTK 3 and **libEGL**. Missing `libEGL.so.1` makes `./monitor` exit immediately.

```bash
# Debian / Ubuntu
sudo apt install libegl1 libgtk-3-0

# Fedora
sudo dnf install mesa-libEGL gtk3
```
