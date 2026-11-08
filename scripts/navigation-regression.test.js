const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const source = fs.readFileSync('web/app.js', 'utf8');
const navigation = source.slice(source.indexOf('let navigationReady ='), source.indexOf('window.addEventListener("popstate"'));

function breadcrumbFixture() {
  const state = {view:'photos', navigation:[], groupScopes:[]};
  const nodes = {query:{value:''}, photoGroup:{value:''}, breadcrumbs:{
    children:[], replaceChildren(){this.children=[];}, append(...items){this.children.push(...items);},
  }};
  const restored = [];
  const context = vm.createContext({state, URLSearchParams, viewTitles:{photos:'All photos',people:'People'},
    $:id=>nodes[id], params:()=>new URLSearchParams({mode:nodes.mode?.value || 'semantic',
      q:nodes.query.value, ...(state.person ? {person:String(state.person.id)} : {})}),
    text:(_tag,label)=>({label}), restoreQuery:key=>restored.push(key), load(){}, window:{scrollTo(){}},
  });
  vm.runInContext(source.slice(source.indexOf('function renderBreadcrumbs()'), source.indexOf('async function enterPhotoGroup(')), context);
  return {state,nodes,restored,render:()=>context.renderBreadcrumbs()};
}

test('text searches replace the current breadcrumb and retain the latest query for restoration', () => {
  const {state,nodes,restored,render} = breadcrumbFixture();
  render();
  for (const query of ['amit','amit stam','birthday']) {
    nodes.query.value=query;
    render();
    render(); // Loading and result renders must not add navigation entries.
    assert.equal(state.navigation.length,1);
    assert.equal(state.navigation[0].label,'Search: '+query);
  }
  nodes.breadcrumbs.children.at(-1).onclick();
  assert.equal(new URLSearchParams(restored[0]).get('q'),'birthday');
  nodes.query.value=''; render();
  assert.equal(state.navigation.length,1);
  assert.equal(state.navigation[0].label,'All photos');
});

test('searching within a person retains People and one person breadcrumb', () => {
  const {state,nodes,render} = breadcrumbFixture();
  state.view='people'; render();
  state.view='photos'; state.person={id:144,name:'Amit'}; render();
  for (const query of ['tree','birthday','amit stam']) {
    nodes.query.value=query; render();
  }
  nodes.mode={value:'metadata'}; render();
  assert.deepEqual(Array.from(state.navigation,e=>e.label),['People','Amit']);
  assert.equal(new URLSearchParams(state.navigation[1].key).get('q'),'amit stam');
  assert.equal(new URLSearchParams(state.navigation[1].key).get('mode'),'metadata');
  state.person={id:16,name:'Alex'}; render();
  assert.deepEqual(Array.from(state.navigation,e=>e.label),['People','Amit','Alex']);
});
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
