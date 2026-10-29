import test from 'node:test';
import assert from 'node:assert/strict';
import {createHash, webcrypto} from 'node:crypto';
import {installFilmFonts} from './film.js';

const names = ['display-cjk.json', 'display-cjk.png', 'body-cjk.json', 'body-cjk.png'];
const sha256 = bytes => createHash('sha256').update(bytes).digest('hex');
const deferred = () => {
  let resolve;
  const promise = new Promise(r => { resolve = r; });
  return {promise, resolve};
};

function fixture({baseURL = 'https://game.example/host.0123456789abcdef/film.js', version = 'first'} = {}) {
  const files = new Map(names.map(name => [name, new TextEncoder().encode(version + ':' + name)]));
  const build = {schema:1, film_cjk:names.map(name => ({name, url:'film/' + name,
    size:files.get(name).length, sha256:sha256(files.get(name))}))};
  const calls = [], delivered = [], failed = [], logs = [], timers = new Map();
  let sequence = 0;
  const win = {
    crypto:webcrypto,
    setTimeout(fn, ms) { assert.equal(ms, 15000); const id = ++sequence; timers.set(id, fn); return id; },
    clearTimeout(id) { timers.delete(id); },
    nanolatheFilmCJKReady(name, bytes) { delivered.push([name, bytes]); },
    nanolatheFilmCJKFailed(message) { failed.push(message); },
    fetch:async (url, options) => {
      calls.push({url:String(url), ...options});
      return serve(url, options);
    },
  };
  let serve = url => String(url).endsWith('/build.json') ? Response.json(build) :
    new Response(files.get(String(url).split('/').pop()));
  const stop = installFilmFonts(win, message => logs.push(message), {baseURL});
  return {win, build, files, calls, delivered, failed, logs, timers, stop, serve:handler => { serve = handler; }};
}

test('Chinese fonts are lazy and share both pending and successful requests', async () => {
  const f = fixture(), manifest = deferred();
  f.serve(async url => String(url).endsWith('/build.json') ? manifest.promise :
    new Response(f.files.get(String(url).split('/').pop())));
  assert.deepEqual(f.calls, [], 'English and Ukrainian request no font data');
  const first = f.win.nanolatheFilmLoadCJK();
  assert.equal(f.win.nanolatheFilmLoadCJK(), first);
  assert.equal(f.calls.length, 1);
  manifest.resolve(Response.json(f.build));
  assert.equal(await first, true);
  assert.equal(f.delivered.length, 4);
  for (const [name, bytes] of f.delivered) assert.deepEqual(bytes, f.files.get(name));
  assert.equal(f.win.nanolatheFilmLoadCJK(), first);
  assert.equal(f.calls.length, 5);
  assert.ok(f.calls.every(call => call.cache === 'force-cache'));
  assert.equal(f.timers.size, 0);
  f.stop();
});

test('no font bytes reach Go until the last body has been read and verified', async () => {
  const f = fixture(), body = deferred(), reading = deferred();
  f.serve(url => {
    if (String(url).endsWith('/build.json')) return Response.json(f.build);
    const name = String(url).split('/').pop();
    return name === 'body-cjk.png' ? {ok:true, arrayBuffer:() => { reading.resolve(); return body.promise; }} :
      new Response(f.files.get(name));
  });
  const pending = f.win.nanolatheFilmLoadCJK();
  await reading.promise;
  assert.deepEqual(f.delivered, []);
  body.resolve(f.files.get('body-cjk.png').buffer);
  assert.equal(await pending, true);
  assert.equal(f.delivered.length, 4);
  f.stop();
});

for (const failure of ['HTTP', 'read', 'size', 'digest']) {
  test('a failed ' + failure + ' check delivers nothing and Apply can retry', async () => {
    const f = fixture();
    let broken = true;
    f.serve(url => {
      if (String(url).endsWith('/build.json')) return Response.json(f.build);
      const name = String(url).split('/').pop(), bytes = f.files.get(name);
      if (broken && name === 'body-cjk.png') {
        if (failure === 'HTTP') return new Response('', {status:503});
        if (failure === 'read') return {ok:true, arrayBuffer:async () => { throw new Error('Read failed'); }};
        if (failure === 'size') return new Response(bytes.slice(1));
        if (failure === 'digest') return new Response(new Uint8Array(bytes.length));
      }
      return new Response(bytes);
    });
    assert.equal(await f.win.nanolatheFilmLoadCJK(), false);
    assert.deepEqual(f.delivered, []);
    assert.equal(f.failed.length, 1);
    assert.deepEqual(f.logs, f.failed);
    assert.equal(f.timers.size, 0);
    assert.ok(f.calls.every(call => call.signal.aborted), 'failed attempt aborts remaining requests');
    const failedCount = f.calls.length;
    broken = false;
    assert.equal(await f.win.nanolatheFilmLoadCJK(), true);
    assert.equal(f.delivered.length, 4);
    assert.ok(f.calls.slice(failedCount).every(call => call.cache === 'reload'), 'retry bypasses bad cached bytes');
    f.stop();
  });
}

test('invalid manifests fail before fetching assets and can be retried', async () => {
  for (const change of [
    build => { build.film_cjk.pop(); },
    build => { build.film_cjk[1] = build.film_cjk[0]; },
    build => { build.film_cjk[0].url = '../film/display-cjk.json'; },
    build => { build.film_cjk[0].url = 'https://other.example/film/display-cjk.json'; },
    build => { build.film_cjk[0].size = -1; },
    build => { build.film_cjk[0].sha256 = 'not a digest'; },
  ]) {
    const f = fixture(), original = structuredClone(f.build);
    change(f.build);
    assert.equal(await f.win.nanolatheFilmLoadCJK(), false);
    assert.equal(f.calls.length, 1);
    assert.deepEqual(f.delivered, []);
    Object.assign(f.build, original);
    assert.equal(await f.win.nanolatheFilmLoadCJK(), true);
    f.stop();
  }
});

test('timeout releases the request, aborts acquisition and ignores its late result', async () => {
  const f = fixture(), stalled = deferred(), requested = deferred();
  f.serve(url => {
    if (String(url).endsWith('/build.json')) return Response.json(f.build);
    if (String(url).endsWith('/body-cjk.png')) { requested.resolve(); return stalled.promise; }
    return new Response(f.files.get(String(url).split('/').pop()));
  });
  const pending = f.win.nanolatheFilmLoadCJK();
  await requested.promise;
  [...f.timers.values()][0]();
  assert.equal(await pending, false);
  assert.match(f.failed[0], /timed out/);
  assert.ok(f.calls.every(call => call.signal.aborted));
  assert.deepEqual(f.delivered, []);
  assert.equal(f.timers.size, 0);
  f.serve(url => String(url).endsWith('/build.json') ? Response.json(f.build) :
    new Response(f.files.get(String(url).split('/').pop())));
  assert.equal(await f.win.nanolatheFilmLoadCJK(), true);
  stalled.resolve(new Response(f.files.get('body-cjk.png')));
  await new Promise(resolve => setImmediate(resolve));
  assert.equal(f.delivered.length, 4, 'late completion from failed attempt delivered nothing');
  f.stop();
});

test('engine exit aborts pending fonts and clears the hook and timer quietly', async () => {
  const f = fixture(), stalled = deferred();
  f.serve(() => stalled.promise);
  const pending = f.win.nanolatheFilmLoadCJK();
  f.stop();
  assert.equal(await pending, false);
  assert.equal(f.win.nanolatheFilmLoadCJK, undefined);
  assert.ok(f.calls[0].signal.aborted);
  assert.equal(f.timers.size, 0);
  assert.deepEqual(f.failed, []);
  assert.deepEqual(f.logs, []);
  stalled.resolve(Response.json(f.build));
  await new Promise(resolve => setImmediate(resolve));
  assert.deepEqual(f.delivered, []);
});

test('new and retained launchers fetch their own atlas versions through the same HTTP cache', async () => {
  const old = fixture(), next = fixture({baseURL:'https://game.example/host.fedcba9876543210/film.js', version:'second'});
  const cache = new Map();
  const serve = (owner, url) => {
    const key = String(url);
    assert.ok(key.startsWith(new URL('.', owner === old ? 'https://game.example/host.0123456789abcdef/film.js' :
      'https://game.example/host.fedcba9876543210/film.js').href));
    if (!cache.has(key)) cache.set(key, key.endsWith('/build.json') ? Response.json(owner.build) :
      new Response(owner.files.get(key.split('/').pop())));
    return cache.get(key).clone();
  };
  old.serve(url => serve(old, url));
  next.serve(url => serve(next, url));
  assert.equal(await old.win.nanolatheFilmLoadCJK(), true);
  assert.equal(await next.win.nanolatheFilmLoadCJK(), true);
  for (const f of [old, next]) {
    for (const [name, bytes] of f.delivered) assert.deepEqual(bytes, f.files.get(name));
    assert.ok(f.calls.every(call => call.cache === 'force-cache'));
  }
  const restarted = fixture();
  restarted.serve(url => serve(old, url));
  assert.equal(await restarted.win.nanolatheFilmLoadCJK(), true);
  for (const [name, bytes] of restarted.delivered) assert.deepEqual(bytes, old.files.get(name));
  assert.equal(cache.size, 10, 'old and new host files occupy distinct cache entries');
  old.stop(); next.stop(); restarted.stop();
});
