const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const source = fs.readFileSync('web/app.js', 'utf8');
const navigation = source.slice(source.indexOf('let navigationReady ='), source.indexOf('window.addEventListener("popstate"'));
test('screen changes preserve history while refreshes and viewer steps replace it', () => {
  const entries = ['/'];
  const location = { pathname: '/', search: '' };
  function save(url) { const u = new URL(url, 'http://localhost'); location.pathname = u.pathname; location.search = u.search; }
  const context = vm.createContext({ location, history: {
    replaceState(_state, _title, url) { entries[entries.length - 1] = url; save(url); },
    pushState(_state, _title, url) { entries.push(url); save(url); },
  } });
  vm.runInContext(navigation, context);
  const write = (url, replace = false) => context.writeNavigationURL(url, replace);
  write('/?view=photos');
  write('/?view=people');
  write('/?view=people');
  write('/?person=1');
  write('/?person=1&image=one');
  write('/?person=1&image=two', true);
  assert.deepEqual(entries, ['/?view=photos', '/?view=people', '/?person=1', '/?person=1&image=two']);
  vm.runInContext('restoringNavigation = true', context);
  write('/?view=photos');
  assert.equal(entries.length, 4);
});
