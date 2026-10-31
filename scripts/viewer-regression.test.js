// Run with node --test scripts/viewer-regression.test.js.
const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const source = fs.readFileSync('web/app.js', 'utf8');
const viewerSource = source.slice(source.indexOf('function viewerSize('), source.indexOf('async function openImage(')) + source.slice(source.indexOf('function sizeViewerImage('), source.indexOf('window.addEventListener("resize"'));
const settle = () => new Promise(resolve => setImmediate(resolve));

function harness() {
  const photos = [
    { id: 'portrait', width: 1884, height: 4080, image_url: '/api/images/portrait/image' },
    { id: 'landscape', width: 4080, height: 1884, image_url: '/api/images/landscape/image' },
    { id: 'other', width: 1884, height: 4080, image_url: '/api/images/other/image' },
  ];
  class Image {
    constructor() {
      this.style = {}; this.dataset = {}; this.classList = new Set();
      this.classList.remove = this.classList.delete.bind(this.classList);
      this.addEventListener = () => {}; this.clientWidth = 500; this.clientHeight = 1200;
    }
    set src(value) {
      this._src = value; this.complete = false;
      queueMicrotask(() => {
        if (this._src !== value) return;
        this.complete = true; this.naturalWidth = 2560;
        this.onload?.();
      });
    }
    get src() { return this._src; }
    removeAttribute(name) { if (name === 'src') this._src = ''; }
  }
  const elements = new Map();
  const $ = id => {
    if (!elements.has(id)) elements.set(id, new Image());
    return elements.get(id);
  };
  $('viewer').open = true;
  const stage = { clientWidth: 2500, clientHeight: 1200, addEventListener() {} };
  const timers = new Map(), requests = [], cache = new Map(), messages = [];
  const state = { items: photos, index: 0, viewerGeneration: 1 };
  const context = vm.createContext({ $, state, Image, AbortController, queueMicrotask,
    innerWidth: 2500, innerHeight: 1200, devicePixelRatio: 1,
    document: { querySelector: () => stage },
    clientThumbnails: cache, currentPhoto: () => photos[state.index],
    placeFaceLayer() {}, stopPlay() {}, params: () => new URLSearchParams(), api: async () => null,
    toast: message => messages.push(message),
    setTimeout(fn, ms) { const id = {}; timers.set(id, { fn, ms }); return id; },
    clearTimeout(id) { timers.delete(id); },
    cacheThumbnail(url, blob) {
      const entry = cache.get(url) || { src: blob.src, expires: Date.now() + 300000 };
      cache.set(url, entry); return entry;
    },
    fetch(url, options) {
      return new Promise((resolve, reject) => {
        const request = { url, options, resolve: () => resolve({ ok: true,
          blob: async () => ({ src: 'blob:' + url.split('&priority=')[0] }) }) };
        requests.push(request);
        options.signal.addEventListener('abort', () => reject(Error('aborted')));
      });
    },
  });
  vm.runInContext(viewerSource + '\nthis.viewerTest = { viewerURL, showViewerPreview, upgradeViewerResolution, clearViewerPreview, zoom: n => viewerZoom = n };', context);
  const navigate = index => { state.index = index; state.viewerGeneration++; context.viewerTest.showViewerPreview(photos[index], state.viewerGeneration, false); };
  return { ...context.viewerTest, context, photos, $, stage, requests, cache, messages, navigate,
    runTimeout() { for (const [id, timer] of timers) if (timer.ms === 30000) { timers.delete(id); timer.fn(); } },
    runUpgrade() { for (const [id, timer] of timers) if (timer.ms === 160) { timers.delete(id); timer.fn(); } } };
}

test('prefetch sizing follows target orientation and uses bounded cache variants', () => {
  const h = harness();
  assert.match(h.viewerURL(h.photos[0]), /width=1280&height=1280&quality=80$/);
  assert.match(h.viewerURL(h.photos[1]), /width=2560&height=2560&quality=80$/);
  h.$('fullImage').clientWidth = 100;
  assert.match(h.viewerURL(h.photos[1]), /width=2560/);
  h.context.devicePixelRatio = 2;
  assert.match(h.viewerURL(h.photos[0]), /width=2560/);
});

test('fullscreen portrait-to-landscape navigation promotes matching prefetch and tracks actual variant', async () => {
  const h = harness();
  h.navigate(0); h.requests[0].resolve(); await settle();
  const prefetch = h.requests[1];
  assert.match(prefetch.url, /landscape\/image\?width=2560&height=2560&quality=80&priority=prefetch$/);
  h.navigate(1);
  assert.equal(prefetch.options.signal.aborted, false);
  const viewer = h.requests[2];
  assert.match(viewer.url, /landscape\/image\?width=2560&height=2560&quality=80&priority=viewer$/);
  viewer.resolve(); await settle();
  assert.equal(h.$('fullImage').dataset.resolution, viewer.url.split('&priority=')[0]);
  assert.equal(h.$('fullImage').classList.has('loading'), false);
  assert.equal(h.$('fullImage').src, 'blob:' + h.$('fullImage').dataset.resolution);
});

test('abandoned navigation cannot overwrite or fail the next photo', async () => {
  const h = harness();
  h.navigate(0); const old = h.requests[0];
  h.navigate(1); const current = h.requests[1];
  assert.equal(old.options.signal.aborted, true);
  old.resolve(); await settle();
  assert.equal(h.$('fullImage').classList.has('loading'), true);
  assert.equal(h.messages.length, 0);
  current.resolve(); await settle();
  assert.match(h.$('fullImage').dataset.resolution, /landscape\/image/);
});

test('zoom while initial image loads replaces the request and clears loading', async () => {
  const h = harness();
  h.navigate(0); const initial = h.requests[0];
  h.zoom(2); h.upgradeViewerResolution(); h.runUpgrade();
  assert.equal(initial.options.signal.aborted, true);
  const upgrade = h.requests[1];
  assert.match(upgrade.url, /width=2560/);
  upgrade.resolve(); await settle();
  assert.equal(h.$('fullImage').classList.has('loading'), false);
  assert.equal(h.$('fullImage').dataset.resolution, upgrade.url.split('&priority=')[0]);
});

test('simultaneous cache fills preserve the object URL being decoded', () => {
  const revoked = [];
  const context = vm.createContext({ clientThumbnails: new Map(), clientThumbnailBytes: 0,
    clientThumbnailBudget: 32 * 1024 * 1024,
    URL: { createObjectURL: blob => blob.id, revokeObjectURL: url => revoked.push(url) } });
  const cacheSource = source.slice(source.indexOf('function cacheThumbnail('), source.indexOf('function requestThumbnail('));
  vm.runInContext(cacheSource, context);
  const first = context.cacheThumbnail('/same-variant', { id: 'blob:first', size: 100 });
  const second = context.cacheThumbnail('/same-variant', { id: 'blob:second', size: 100 });
  assert.equal(second.src, first.src);
  assert.deepEqual(revoked, []);
  assert.equal(context.clientThumbnailBytes, 100);
});


test('zoom at the maximum variant keeps the existing in-flight request', () => {
  const h = harness();
  h.navigate(1);
  h.zoom(2); h.upgradeViewerResolution(); h.runUpgrade();
  assert.equal(h.requests.length, 1);
  assert.equal(h.requests[0].options.signal.aborted, false);
});

test('stalled preview exits loading state and can be retried', async () => {
  const h = harness();
  h.navigate(0); h.runTimeout(); await settle();
  assert.equal(h.requests[0].options.signal.aborted, true);
  assert.equal(h.$('fullImage').classList.has('loading'), false);
  assert.equal(h.messages.length, 1);
  h.upgradeViewerResolution(); h.runUpgrade();
  assert.equal(h.requests.length, 2);
  h.requests[1].resolve(); await settle();
  assert.match(h.$('fullImage').dataset.resolution, /portrait\/image/);
});
