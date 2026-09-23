#!/usr/bin/env python3
"""Check Darwin process identity and exit handling against an isolated daemon."""

import argparse
import json
import os
from pathlib import Path
import platform
import shutil
import signal
import socket
import subprocess
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", type=Path, default=Path(__file__).resolve().parents[1] / "core/bin/monitord")
    parser.add_argument("--require-clean-startup", action="store_true",
                        help="also fail on any collector error before the first healthy collection")
    args = parser.parse_args()
    if platform.system() != "Darwin":
        parser.error("this check requires macOS; portable collector tests run with go test")
    binary = args.binary.resolve(strict=True)
    with tempfile.TemporaryDirectory(prefix="monitor-apps-identity-") as temporary:
        root = Path(temporary)
        names = ("monitor-short", "monitor identity long name")
        config = root / "config.yaml"
        config.write_text(
            "collectors:\n  enabled: [cpu, mem, apps]\n  modules:\n    apps:\n"
            "      defaults: false\n      top: 10000\n      groups:\n        identity_fixture: " + json.dumps(names) + "\n"
            "plugins:\n  enabled: false\nhealth:\n  enabled: false\n"
        )
        with socket.socket() as probe:
            probe.bind(("127.0.0.1", 0))
            port = probe.getsockname()[1]
        base = f"http://127.0.0.1:{port}"
        owned = []
        daemon = None
        token = ""

        def stop(process):
            if process is not None and process.poll() is None:
                process.send_signal(signal.SIGINT)
                try:
                    process.wait(timeout=15)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait()
                    raise

        def read(path, authenticated=True):
            headers = {"Authorization": "Bearer " + token} if authenticated else {}
            with urllib.request.urlopen(urllib.request.Request(base + path, headers=headers), timeout=10) as response:
                return json.load(response)

        def eventually(check, description):
            deadline = time.monotonic() + 20
            while time.monotonic() < deadline:
                if daemon.poll() is not None:
                    raise AssertionError("test daemon exited before " + description)
                if check():
                    return
                time.sleep(.2)
            raise AssertionError("timeout waiting for " + description)

        with (root / "server.log").open("w") as log:
            try:
                for name in names:
                    executable = root / name
                    # Copy bytes only; macOS system-file flags are not suitable
                    # for an ordinary temporary fixture executable.
                    shutil.copyfile("/bin/sleep", executable)
                    executable.chmod(0o700)
                    # A relocated system binary can otherwise be killed by
                    # macOS signature validation after it initially appears.
                    subprocess.run(["/usr/bin/codesign", "--force", "--sign", "-", str(executable)],
                                   check=True, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
                    owned.append(subprocess.Popen([str(executable), "120"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL))
                launched_at = time.monotonic()
                daemon = subprocess.Popen([str(binary), "-config", str(config), "-data-dir", str(root / "data"),
                                           "-listen", f"127.0.0.1:{port}"], stdout=log, stderr=log)

                def ready():
                    nonlocal token
                    try:
                        token = (root / "data/web-password").read_text().strip()
                        read("/api/v1/info")
                        return True
                    except (OSError, urllib.error.URLError):
                        return False

                eventually(ready, "authenticated readiness")
                query = "/api/v1/function?" + urllib.parse.urlencode({"function": "processes", "group": "identity_fixture"})
                try:
                    read(query, authenticated=False)
                except urllib.error.HTTPError as error:
                    assert error.code == 401, "process table did not reject unauthenticated access"
                else:
                    raise AssertionError("process table allowed unauthenticated access")

                observed_times = set()

                def rows():
                    result = read(query)["result"]
                    if result["rows"]:
                        timestamp = result.get("collected_at")
                        assert isinstance(timestamp, int) and timestamp > 0, "process snapshot has no collection timestamp"
                        observed_times.add(timestamp)
                    return {row["pid"]: row for row in result["rows"]}

                def apps_status():
                    return next(item for item in read("/api/v1/collectors")["status"] if item["name"] == "apps")

                def both_present():
                    if any(process.poll() is not None for process in owned):
                        raise AssertionError("a fixture process exited before identity verification")
                    current = rows()
                    return all(process.pid in current for process in owned)

                try:
                    eventually(both_present, "both fixture identities")
                except AssertionError:
                    # Only report this script's own fixture identities. Never
                    # dump arbitrary process arguments or collector errors.
                    own_ids = {process.pid for process in owned}
                    visible = read("/api/v1/function?function=processes")["result"]["rows"]
                    status = apps_status()
                    print(json.dumps({"fixture_states": [process.poll() for process in owned],
                                      "visible_fixtures": [{key: row[key] for key in ("pid", "name", "group")}
                                                           for row in visible if row["pid"] in own_ids],
                                      "apps_status": {key: status.get(key) for key in ("enabled", "runs", "failures", "last_run_ms")}}))
                    raise
                # Cold-start owner/identity discovery can exceed the default
                # one-second collection budget on a busy host. Record those
                # failures separately; only start the steady-state check once
                # the collector has completed a healthy, full collection.
                initial_status = None

                def initialized():
                    nonlocal initial_status
                    initial_status = apps_status()
                    return initial_status["enabled"] and initial_status["runs"] > 0 and not initial_status.get("error")

                eventually(initialized, "first healthy complete apps collection")
                startup_failures = initial_status["failures"]
                startup_elapsed_ms = round((time.monotonic() - launched_at) * 1000)
                for _ in range(3):
                    current = rows()
                    for name, process in zip(names, owned):
                        row = current[process.pid]
                        assert row["name"] == name, "full process name changed"
                        assert row["cmdline"] == str(root / name) + " 120", "command line changed"
                        assert row["ppid"] == os.getpid() and row["group"] == "identity_fixture", "owner/group changed"
                        assert row["rss"] > 0 and row["threads"] > 0 and row["cpu"] >= 0, "fixture counters unavailable"
                    time.sleep(1.1)
                owned[0].terminate()
                owned[0].wait(timeout=5)

                def removed():
                    current = rows()
                    return owned[0].pid not in current and owned[1].pid in current

                eventually(removed, "exited PID removal while the live PID remains")
                assert len(observed_times) >= 2, "process snapshot timestamp did not advance"
                # Query the exact observed sampling window, including the exit
                # snapshot, instead of a wall-clock-relative range that can
                # move while the test is running on a busy host.
                history_after, history_before = min(observed_times) - 1, max(observed_times) + 1
                data = read("/api/v1/data?" + urllib.parse.urlencode({
                    "chart": "apps.processes", "after": history_after, "before": history_before,
                    "points": history_before - history_after,
                }))
                column = data["dimension_ids"].index("identity_fixture") + 1
                counts = [row[column] for row in data["result"]["data"] if row[column] is not None]
                assert 2 in counts and 1 in counts, f"fixture history counts={counts}; window={history_after}:{history_before}"
                status = apps_status()
                assert status["enabled"] and status["runs"] >= 3 and not status.get("error"), "apps collector unavailable"
                failure_lines = [line for line in (root / "server.log").read_text().splitlines()
                                 if "collector: failed" in line and "name=apps" in line]
                failure_kinds = [kind for kind in ("context deadline exceeded", "context canceled", "cannot allocate memory")
                                 if any(kind in line for line in failure_lines)]
                steady_failures = status["failures"] - startup_failures
                assert steady_failures == 0, f"apps collector steady failures={steady_failures}; known causes={failure_kinds}"
                if args.require_clean_startup:
                    assert startup_failures == 0, f"apps collector startup failures={startup_failures}; known causes={failure_kinds}"
                stop(daemon)
                assert daemon.returncode == 0, "unclean daemon exit"
                print(json.dumps({"identities_verified": len(names), "repeated_samples": 3,
                                  "exit_removal": True, "history_counts": [2, 1], "unauthenticated_status": 401,
                                  "collector_runs": status["runs"], "collector_failures": status["failures"],
                                  "startup_failures": startup_failures, "steady_failures": steady_failures,
                                  "startup_observed_ms": startup_elapsed_ms,
                                  "observed_healthy_run_ms": initial_status["last_run_ms"],
                                  "runs_at_first_observation": initial_status["runs"],
                                  "snapshot_times_verified": len(observed_times),
                                  "known_failure_causes": failure_kinds,
                                  "clean_shutdown": True}))
            finally:
                try:
                    stop(daemon)
                finally:
                    for process in owned:
                        stop(process)


if __name__ == "__main__":
    main()
