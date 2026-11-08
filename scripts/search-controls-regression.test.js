const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const source = fs.readFileSync('web/app.js', 'utf8');
function extract(start, end) {
  return source.slice(source.indexOf(start), source.indexOf(end, source.indexOf(start)));
}

test('reloaded semantic links honor date ordering and enable direction controls', () => {
  const state = {};
  const nodes = Object.fromEntries(['query','mode','photoSort','sortDirection','photoGroup'].map(id=>[id,{value:''}]));
  nodes.photoSort.options = ['filename','taken_at','relevance'].map(value=>({value}));
  nodes.photoGroup.options = ['','year'].map(value=>({value}));
  const context = vm.createContext({state,fields:{},URLSearchParams,$:id=>nodes[id]});
  vm.runInContext(extract('function resetInputs()', 'const viewTitles ='),context);
  vm.runInContext(extract('function updatePhotoOrdering()', 'function viewChrome()'),context);
  context.restoreQuery('mode=semantic&q=throwing+kids+up+to+the+sky&people_filter=%5B%22144%22%2C%223944%22%5D&sort=taken_at&direction=desc&layout=grid&view=photos&offset=0');
  context.updatePhotoOrdering();
  assert.equal(nodes.photoSort.value,'taken_at');
  assert.equal(nodes.sortDirection.value,'desc');
  assert.equal(nodes.photoSort.disabled,false);
  assert.equal(nodes.sortDirection.disabled,false);
  assert.deepEqual(Array.from(state.peopleFilters,p=>p.id),[144,3944]);
  nodes.sortDirection.value='asc'; context.updatePhotoOrdering();
  assert.equal(nodes.sortDirection.disabled,false);
  nodes.photoSort.value='relevance'; context.updatePhotoOrdering();
  assert.equal(nodes.sortDirection.disabled,true);
  nodes.photoGroup.value='year'; context.updatePhotoOrdering();
  assert.equal(nodes.photoSort.disabled,true);
  assert.equal(nodes.sortDirection.disabled,false);
  nodes.mode.value='metadata'; context.updatePhotoOrdering();
  assert.equal(nodes.photoSort.value,'taken_at');
  assert.equal(nodes.sortDirection.disabled,false);
  assert.equal(nodes.photoSort.options.at(-1).hidden,true);
});

test('Find similar keeps filters, selects relevance, and restores its source from a link', () => {
  const nodes=Object.fromEntries(['query','mode','photoSort','sortDirection','photoGroup'].map(id=>[id,{value:''}]));
  nodes.photoSort.options=['filename','taken_at','relevance'].map(value=>({value}));
  nodes.photoGroup.options=['','year'].map(value=>({value}));
  const state={peopleFilters:[{id:144}],groupScopes:[],layout:'grid',navigation:[],dirty:false};
  let loaded=0;
  const context=vm.createContext({state,fields:{},URLSearchParams,$:id=>nodes[id],currentPhoto:()=>({id:'source-photo'}),
    stopPlay(){},closeModal(){},load(){loaded++;},window:{scrollTo(){}},confirm(){return true;}});
  vm.runInContext('let closingViewerForNavigation=false;'+extract('function findSimilarPhotos()', '$("findSimilar").onclick'),context);
  context.findSimilarPhotos();
  assert.equal(loaded,1);
  assert.equal(state.similarTo,'source-photo');
  assert.equal(state.peopleFilters[0].id,144);
  assert.equal(nodes.query.value,'');
  assert.equal(nodes.photoSort.value,'relevance');
  vm.runInContext(extract('function params()', 'let navigationReady ='),context);
  const url=context.params().toString();
  vm.runInContext(extract('function resetInputs()', 'const viewTitles ='),context);
  context.restoreQuery(url);
  assert.equal(state.similarTo,'source-photo');
  assert.equal(state.peopleFilters[0].id,144);
  assert.equal(nodes.photoSort.value,'relevance');
  assert.equal(context.params().get('similar_to'),'source-photo');
  state.dirty=true;
  context.confirm=()=>false;
  context.findSimilarPhotos();
  assert.equal(loaded,1);
});

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
