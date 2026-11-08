const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const source = fs.readFileSync('web/app.js', 'utf8');
function extract(start, end) {
  return source.slice(source.indexOf(start), source.indexOf(end, source.indexOf(start)));
}

test('sidebar navigation starts a fresh breadcrumb and clears person and grouping scopes', () => {
  const state = { navigation: [{label:'People'}, {label:'Michal'}], person: {id:956},
    peopleFilters: [{id:2}], groupScopes: [{kind:'year',key:'2024'}], selected: new Set([1]) };
  const nodes = Object.fromEntries(['photoGroup','query','directory'].map(id => [id, {value:'old'}]));
  const context = vm.createContext({state, fields:{directory:'directory'}, $:id=>nodes[id], stopPlay(){},
    viewChrome(){ state.navigation.push({label:state.view}); }, load(){} });
  vm.runInContext(extract('function resetInputs()', 'function restoreQuery'), context);
  vm.runInContext(extract('function setView(', 'function gridCap'), context);
  for (const view of ['favorites','people','albums','favorites','people']) {
    context.setView(view);
    assert.deepEqual(Array.from(state.navigation, e=>e.label), [view]);
    assert.equal(state.person, null);
    assert.equal(state.peopleFilters.length, 0);
    assert.equal(state.groupScopes.length, 0);
    assert.equal(nodes.photoGroup.value, '');
    assert.equal(nodes.query.value, '');
  }
});

test('unavailable content search keeps semantic mode and selected people without retrying metadata', async () => {
  const requests = [];
  const context = vm.createContext({ URLSearchParams, api: async path => {
    requests.push(new URL(path, 'http://localhost').searchParams);
    throw Object.assign(Error('Content search unavailable'), {status:503});
  }});
  vm.runInContext(extract('const gridPages =', 'function filterChips'), context);
  const p = new URLSearchParams({mode:'semantic',q:'tree',person:'956',people_filter:'["1","2"]'});
  await assert.rejects(context.gridPage(p), /Content search unavailable/);
  assert.equal(requests.length, 1);
  assert.equal(requests[0].get('mode'), 'semantic');
  assert.equal(requests[0].get('people_filter'), '["1","2"]');
  assert.equal(p.get('mode'), 'semantic');
});

test('search errors other than unavailable content search propagate without changing the query', async () => {
  for (const [mode,status] of [['semantic',400],['metadata',503]]) {
    let calls = 0;
    const context = vm.createContext({URLSearchParams, api:async()=>{
      calls++; throw Object.assign(Error('original error'), {status});
    }});
    vm.runInContext(extract('const gridPages =', 'function filterChips'), context);
    await assert.rejects(context.gridPage(new URLSearchParams({mode,q:'beach'})), /original error/);
    assert.equal(calls, 1);
  }
});
