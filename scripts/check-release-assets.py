#!/usr/bin/env python3
"""Require the complete release matrix before publishing checksummed assets."""
import argparse
import hashlib
from pathlib import Path
import re


def verify(directory, tag):
    if not re.fullmatch(r'v(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)', tag):
        raise ValueError('expected stable vMAJOR.MINOR.PATCH tag')
    expected = {f'monitord-{tag}-{platform}.{ext}' for platform, ext in [
        ('linux-amd64', 'tar.gz'), ('linux-arm64', 'tar.gz'),
        ('macos-amd64', 'tar.gz'), ('macos-arm64', 'tar.gz'),
        ('freebsd-amd64', 'tar.gz'), ('freebsd-arm64', 'tar.gz'), ('windows-amd64', 'zip')]}
    expected.update(f'monitor-app-{tag}-{platform}.{ext}' for platform, ext in [
        ('android', 'apk'), ('ios-unsigned', 'ipa'), ('linux-amd64', 'tar.gz'), ('windows-amd64', 'zip'),
        ('macos-amd64', 'dmg'), ('macos-amd64', 'zip'), ('macos-arm64', 'dmg'), ('macos-arm64', 'zip')])
    actual = {p.name for p in directory.iterdir() if p.name != 'SHA256SUMS.txt'}
    android = {f'monitord-{tag}-android-arm64{suffix}.apk' for suffix in ['', '-unsigned']}
    selected = actual & android
    if len(selected) != 1:
        raise ValueError('expected exactly one signed or unsigned Android server APK')
    expected.update(selected)
    if actual != expected:
        raise ValueError(f'asset matrix mismatch; missing={sorted(expected-actual)}, unexpected={sorted(actual-expected)}')
    lines = []
    for name in sorted(expected):
        path = directory / name
        if path.is_symlink() or not path.is_file() or not path.stat().st_size:
            raise ValueError(f'asset must be a nonempty regular file: {name}')
        digest = hashlib.sha256()
        with path.open('rb') as source:
            for chunk in iter(lambda: source.read(1024 * 1024), b''):
                digest.update(chunk)
        lines.append(f'{digest.hexdigest()}  {name}\n')
    checksum = directory / 'SHA256SUMS.txt'
    if checksum.is_symlink():
        raise ValueError('checksum output must not be a symlink')
    checksum.write_text(''.join(lines))
    return len(expected)


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('directory', type=Path)
    parser.add_argument('tag')
    args = parser.parse_args()
    print(f'Verified {verify(args.directory, args.tag)} release assets')
