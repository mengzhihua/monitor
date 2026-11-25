import importlib.util
from pathlib import Path
import tempfile
import unittest

spec = importlib.util.spec_from_file_location('assets', Path(__file__).with_name('check-release-assets.py'))
assets = importlib.util.module_from_spec(spec)
spec.loader.exec_module(assets)


class AssetsTest(unittest.TestCase):
    def fixture(self, root):
        for platform in ['linux-amd64', 'linux-arm64', 'macos-amd64', 'macos-arm64', 'freebsd-amd64', 'freebsd-arm64']:
            (root / f'monitord-v3.0.21-{platform}.tar.gz').write_bytes(b'fixture')
        for name in ['monitord-v3.0.21-windows-amd64.zip', 'monitord-v3.0.21-android-arm64-unsigned.apk',
                     *[f'monitor-app-v3.0.21-{p}' for p in ['android.apk', 'ios-unsigned.ipa', 'linux-amd64.tar.gz', 'windows-amd64.zip', 'macos-amd64.dmg', 'macos-amd64.zip', 'macos-arm64.dmg', 'macos-arm64.zip']]]:
            (root / name).write_bytes(b'fixture')

    def test_complete_and_signed_variants(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp); self.fixture(root)
            self.assertEqual(assets.verify(root, 'v3.0.21'), 16)
            self.assertEqual(len((root / 'SHA256SUMS.txt').read_text().splitlines()), 16)
            (root / 'monitord-v3.0.21-android-arm64-unsigned.apk').rename(root / 'monitord-v3.0.21-android-arm64.apk')
            self.assertEqual(assets.verify(root, 'v3.0.21'), 16)

    def test_incomplete_unsafe_or_mixed_assets_fail(self):
        for kind in ['missing', 'empty', 'extra', 'duplicate_android', 'symlink', 'checksum_symlink', 'wrong_version']:
            with self.subTest(kind=kind), tempfile.TemporaryDirectory() as tmp:
                root = Path(tmp); self.fixture(root)
                target = root / 'monitor-app-v3.0.21-android.apk'
                if kind == 'missing': target.unlink()
                if kind == 'empty': target.write_bytes(b'')
                if kind == 'extra': (root / 'private.txt').write_text('unwanted')
                if kind == 'duplicate_android': (root / 'monitord-v3.0.21-android-arm64.apk').write_bytes(b'x')
                if kind == 'symlink':
                    target.unlink(); target.symlink_to(root / 'monitor-app-v3.0.21-ios-unsigned.ipa')
                if kind == 'checksum_symlink': (root / 'SHA256SUMS.txt').symlink_to(target)
                with self.assertRaises(ValueError): assets.verify(root, 'v3.0.20' if kind == 'wrong_version' else 'v3.0.21')


if __name__ == '__main__': unittest.main()
