"""Pin every host module and child page to the same engine build."""
import hashlib
import json
from pathlib import Path
from typing import Optional

FILM_CJK = ('display-cjk.json', 'display-cjk.png', 'body-cjk.json', 'body-cjk.png')


def browser_bundle(source: Path, out: Path, build: dict, font_source: Optional[Path] = None) -> str:
    files = sorted(p for p in source.iterdir()
                   if p.suffix in ('.js', '.html', '.css')
                   and p.name != 'wasm_exec.js' and '.test.' not in p.name)
    # The font digests participate in the host name. Keeping their bytes
    # inside that directory preserves the old host's fonts on a later build.
    fonts = ({'film/' + name: (font_source / name).read_bytes() for name in FILM_CJK}
             if font_source is not None else {})
    build = dict(build)
    if fonts:
        build['film_cjk'] = [
            dict(name=Path(path).name, url=path, size=len(data), sha256=hashlib.sha256(data).hexdigest())
            for path, data in fonts.items()
        ]
    encoded = (json.dumps(build, sort_keys=True, indent=2) + '\n').encode()
    digest = hashlib.sha256(encoded)
    for path in files:
        digest.update(path.name.encode() + b'\0' + path.read_bytes())
    name = 'host.' + digest.hexdigest()[:16]
    bundle = out / name
    bundle.mkdir(parents=True, exist_ok=True)
    for path in files:
        (bundle / path.name).write_bytes(path.read_bytes())
    for path, data in fonts.items():
        destination = bundle / path
        destination.parent.mkdir(parents=True, exist_ok=True)
        destination.write_bytes(data)
    (bundle / 'build.json').write_bytes(encoded)
    # The root manifest also names the host directory and its files, so a
    # deployment can retain the previous build for launchers that are still
    # open (DESIGN_BROWSER_HOST §4 contract 7). The pinned copy inside the
    # host directory stays free of that self-reference.
    root = dict(build, host=name, files=[p.name for p in files] + ['build.json'] + list(fonts))
    (out / 'build.json').write_bytes((json.dumps(root, sort_keys=True, indent=2) + '\n').encode())
    index = (source / 'index.html').read_text()
    index = index.replace('href="style.css"', f'href="{name}/style.css"')
    index = index.replace('src="launcher.js"', f'src="{name}/launcher.js"')
    (out / 'index.html').write_text(index)
    return name
