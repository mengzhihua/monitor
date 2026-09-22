# Monitor Linux client

Extract the tarball and run `./monitor` from the bundle directory.

## Runtime libraries

Flutter's Linux embedder needs GTK and **libEGL**. Missing `libEGL.so.1` makes the window exit immediately.

Debian / Ubuntu:

```bash
sudo apt install libegl1 libgtk-3-0
```

Fedora:

```bash
sudo dnf install mesa-libEGL gtk3
```

Then:

```bash
./monitor
```
