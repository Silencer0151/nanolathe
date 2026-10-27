import {BrowserFS} from './fs.js';
import {BrowserStorage} from './storage.js';
import {installBrowserGestures} from './gestures.js';
import {installFrameGate} from './frames.js';
import {installBrowserClipboard} from './clipboard.js';

const run = new URLSearchParams(location.search).get('run');
const host = parent.nanolatheBrowserHost;
const config = host?.getLaunch(run);
const post = (type, payload) => parent.postMessage({channel:'nanolathe', run, type, payload}, location.origin);
if (config) {
  // Online play keeps a hidden page in step between the engine's frames.
  installFrameGate(window);
  const stopGestures = installBrowserGestures(window, event => window.nanolatheBrowserGesture?.(event));
  const stopClipboard = installBrowserClipboard(window, text => window.nanolatheBrowserPaste?.(text));
  try {
    const storage = new BrowserStorage(config.storage, () => post('storage'));
    const install = new BrowserFS(config.files, storage, text => post('log', text), result => post('persistence', result));
    await install.restore();
    window.fs = install.fs; window.process = install.process; window.path = install.path;
    const build = await config.module;
    await new Promise((resolve, reject) => {
      const script = document.createElement('script'); script.src = build.runtime;
      script.onload = resolve; script.onerror = () => reject(new Error('Could not load the matching Go browser runtime.'));
      document.head.append(script);
    });
    const go = new Go();
    go.env = {HOME:'/settings', TMPDIR:'/tmp', NANOLATHE_SETTINGS:'/settings/settings.json',
      GOGC:config.memory === 'lower' ? '50' : '100', NANOLATHE_BROWSER_MEMORY:config.memory};
    go.argv = ['nanolathe', '--root=/game', '--save-dir=/saves', '--mod=none', '--fullscreen=false',
      '--auto-remaster=false', '--renderer=' + config.renderer, '--fps=60'];
    if (config.entry === 'demo') go.argv.push('--mission=camps/Arm Campaign.tdf:MISSION0');
    if (config.entry === 'skirmish' || config.entry === 'stress') go.argv.push('--map=' + config.map, '--seed=7');
    if (config.entry === 'stress') go.env.NANOLATHE_BROWSER_SCENE = 'field:250';
    post('status', 'Restored browser files. Preparing WebAssembly…');
    const instance = await WebAssembly.instantiate(build.module, go.importObject);
    window.nanolatheBrowserSample = encoded => post('sample', {...JSON.parse(encoded),
      wasm_mib:instance.exports.mem.buffer.byteLength / 1024**2, cache_mib:install.cacheBytes / 1024**2});
    window.nanolatheBrowserHeapReady = (bytes, error) => post('profile', {bytes, error});
    // The Chinese atlases are not in the wasm: the engine asks for them when a
    // language needs them, and this hands back the four files it reads.
    window.nanolatheFilmLoadCJK = () => {
      const names = ['display-cjk.json', 'display-cjk.png', 'body-cjk.json', 'body-cjk.png'];
      Promise.all(names.map(async name => {
        const response = await fetch(new URL('../film/' + name, import.meta.url), {cache: 'force-cache'});
        if (!response.ok) throw new Error('film/' + name + ': HTTP ' + response.status);
        window.nanolatheFilmCJKReady?.(name, new Uint8Array(await response.arrayBuffer()));
      })).catch(error => post('log', 'Chinese atlases: ' + (error.stack || error.message)));
    };
    window.addEventListener('message', event => {
      if (event.source === parent && event.origin === location.origin && event.data?.channel === 'nanolathe' &&
          event.data.run === run && event.data.type === 'heap-profile') window.browserHeapProfile?.();
    });
    post('status', 'Starting ' + config.name + '…');
    await go.run(instance);
    post('stopped', 'Engine exited. Restart to play again.');
  } catch (error) {
    post('log', error.stack || error.message);
    post('stopped', 'Startup failed: ' + error.message);
  } finally {
    stopGestures();
    stopClipboard();
  }
}
