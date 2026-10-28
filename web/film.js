// Optional presentation assets stay pinned to this module's host directory
// (DESIGN_BROWSER_HOST §4 contract 7), and are requested only by the engine.
const names = ['display-cjk.json', 'display-cjk.png', 'body-cjk.json', 'body-cjk.png'];
const hex = bytes => Array.from(new Uint8Array(bytes), n => n.toString(16).padStart(2, '0')).join('');

async function fetchFonts(win, base, signal, cache) {
  const response = await win.fetch(new URL('build.json', base), {signal, cache});
  if (!response.ok) throw new Error('Font manifest: HTTP ' + response.status);
  const build = await response.json(), files = build.film_cjk;
  signal.throwIfAborted();
  if (build.schema !== 1 || !Array.isArray(files) || files.length !== names.length) {
    throw new Error('Invalid font manifest.');
  }
  const entries = new Map();
  for (const file of files) {
    if (!file || !names.includes(file.name) || entries.has(file.name) || file.url !== 'film/' + file.name ||
        !Number.isSafeInteger(file.size) || file.size <= 0 || !/^[a-f0-9]{64}$/.test(file.sha256)) {
      throw new Error('Invalid font manifest.');
    }
    entries.set(file.name, file);
  }
  // Stage the complete pair before Go sees any bytes. Failed reads, integrity
  // checks and timed-out attempts can never leave a partially usable font set.
  return Promise.all(names.map(async name => {
    const file = entries.get(name);
    const response = await win.fetch(new URL(file.url, base), {signal, cache});
    if (!response.ok) throw new Error(file.url + ': HTTP ' + response.status);
    const bytes = new Uint8Array(await response.arrayBuffer());
    signal.throwIfAborted();
    if (bytes.byteLength !== file.size || hex(await win.crypto.subtle.digest('SHA-256', bytes)) !== file.sha256) {
      throw new Error('Font integrity check failed: ' + name);
    }
    return [name, bytes];
  }));
}

// installFilmFonts installs the Go request hook before go.run. Pending requests
// share one attempt; success is kept, and a failure lets the next Apply retry.
// Cleanup retires an in-flight attempt without reporting an engine-exit error.
export function installFilmFonts(win, log, {baseURL = import.meta.url, timeoutMs = 15000} = {}) {
  const base = new URL('.', baseURL);
  let request = null, cancel = null, stopped = false, retry = false;
  const load = () => {
    if (stopped) return Promise.resolve(false);
    if (request) return request;
    const controller = new AbortController();
    let rejectAttempt;
    const cancelled = new Promise((_, reject) => { rejectAttempt = reject; });
    const abort = error => { controller.abort(error); rejectAttempt(error); };
    cancel = abort;
    const timer = win.setTimeout(() => abort(new Error('Font download timed out. Try Apply again.')), timeoutMs);
    const task = Promise.race([fetchFonts(win, base, controller.signal, retry ? 'reload' : 'force-cache'), cancelled])
      .then(files => {
        if (stopped) return false;
        for (const [name, bytes] of files) win.nanolatheFilmCJKReady(name, bytes);
        return true;
      })
      .catch(error => {
        controller.abort();
        if (request === task) request = null;
        retry = true;
        if (!stopped) {
          const message = 'Chinese fonts: ' + (error.message || String(error));
          log(message);
          win.nanolatheFilmCJKFailed?.(message);
        }
        return false;
      })
      .finally(() => {
        win.clearTimeout(timer);
        if (cancel === abort) cancel = null;
      });
    request = task;
    return task;
  };
  win.nanolatheFilmLoadCJK = load;
  return () => {
    stopped = true;
    cancel?.(new DOMException('Cancelled', 'AbortError'));
    if (win.nanolatheFilmLoadCJK === load) delete win.nanolatheFilmLoadCJK;
  };
}
