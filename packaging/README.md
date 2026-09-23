# Packaging

`monitord.service` is a systemd unit for a static `monitord` binary installed at `/usr/bin/monitord`.

```
install -d /etc/monitor /var/lib/monitor
install -m 644 monitor.example.yaml /etc/monitor/monitor.yaml
install -m 755 core/bin/monitord /usr/bin/monitord
install -m 644 packaging/monitord.service /etc/systemd/system/monitord.service
systemctl daemon-reload
systemctl enable --now monitord
```

The unit does not ship a package build. Release artifacts stay in `.github/workflows/release.yml`.
