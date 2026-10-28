"""Authored packaging fixtures; no original game assets."""
import hashlib
import json
import tempfile
import unittest
from pathlib import Path
from browser_bundle import browser_bundle


class BundleTests(unittest.TestCase):
    def test_old_launcher_keeps_its_child_modules_and_engine_manifest(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            source, out = root / 'web', root / 'out'
            source.mkdir()
            out.mkdir()
            (source / 'index.html').write_text('<link href="style.css"><script src="launcher.js"></script>')
            (source / 'launcher.js').write_text('import "./host.js";')
            (source / 'host.js').write_text('old host')
            (source / 'game.html').write_text('<script src="game.js"></script>')
            (source / 'game.js').write_text('old child')
            (source / 'host.test.mjs').write_text('test')
            build = dict(wasm='old.wasm', runtime='old.js', schema=1)
            first = browser_bundle(source, out, build)
            self.assertEqual(first, browser_bundle(source, out, build))
            (source / 'game.js').write_text('new child')
            second = browser_bundle(source, out, dict(build, wasm='new.wasm'))
            self.assertNotEqual(first, second)
            self.assertEqual((out / first / 'game.js').read_text(), 'old child')
            self.assertIn('old.wasm', (out / first / 'build.json').read_text())
            self.assertIn(second + '/launcher.js', (out / 'index.html').read_text())
            self.assertIn(second + '/style.css', (out / 'index.html').read_text())
            self.assertFalse((out / first / 'host.test.mjs').exists())
            root_manifest = json.loads((out / 'build.json').read_text())
            self.assertEqual(root_manifest['host'], second)
            self.assertEqual(root_manifest['files'], ['build.json', 'game.html', 'game.js', 'host.js', 'index.html', 'launcher.js'][1:] + ['build.json'])
            self.assertNotIn('host', json.loads((out / second / 'build.json').read_text()))

    def test_font_bytes_and_metadata_stay_pinned_to_each_host(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            source, out, fonts = root / 'web', root / 'out', root / 'fonts'
            for path in (source, out, fonts):
                path.mkdir()
            (source / 'index.html').write_text('<link href="style.css"><script src="launcher.js"></script>')
            (source / 'film.js').write_text('authored loader fixture')
            names = ('display-cjk.json', 'display-cjk.png', 'body-cjk.json', 'body-cjk.png')
            for name in names:
                (fonts / name).write_bytes(('first:' + name).encode())
            build = dict(wasm='same.wasm', runtime='same.js', schema=1)
            first = browser_bundle(source, out, build, fonts)
            self.assertEqual(first, browser_bundle(source, out, build, fonts))
            first_manifest = json.loads((out / first / 'build.json').read_text())
            self.assertEqual({file['name'] for file in first_manifest['film_cjk']}, set(names))
            for file in first_manifest['film_cjk']:
                self.assertEqual(file['url'], 'film/' + file['name'])
                data = (out / first / file['url']).read_bytes()
                self.assertEqual(file['size'], len(data))
                self.assertEqual(file['sha256'], hashlib.sha256(data).hexdigest())
            before = (out / first / 'film/body-cjk.png').read_bytes()
            (fonts / 'body-cjk.png').write_bytes(b'second body atlas')
            second = browser_bundle(source, out, build, fonts)
            self.assertNotEqual(first, second, 'font bytes participate in the host hash')
            self.assertEqual((out / first / 'film/body-cjk.png').read_bytes(), before)
            self.assertEqual(json.loads((out / first / 'build.json').read_text()), first_manifest)
            second_manifest = json.loads((out / second / 'build.json').read_text())
            self.assertNotEqual(first_manifest['film_cjk'], second_manifest['film_cjk'])
            root_manifest = json.loads((out / 'build.json').read_text())
            self.assertEqual(root_manifest['film_cjk'], second_manifest['film_cjk'])
            for name in names:
                self.assertIn('film/' + name, root_manifest['files'])
                self.assertTrue((out / second / 'film' / name).is_file())
            self.assertFalse((out / 'film').exists(), 'there is no shared mutable font directory')


if __name__ == '__main__':
    unittest.main()
