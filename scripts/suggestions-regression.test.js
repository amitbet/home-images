const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const source = fs.readFileSync('web/app.js', 'utf8');
const flow = source.slice(source.indexOf('async function loadPersonSuggestions('), source.indexOf('\nfunction confirmPersonAction('));
const items = (start, count) => Array.from({length: count}, (_, i) => ({id: start+i}));
function harness(api) {
  const nodes = new Map();
  const context = vm.createContext({AbortController, api,
    $: id => {if(!nodes.has(id)) nodes.set(id,{value:'all',setAttribute(){},replaceChildren(){}});return nodes.get(id);},
    text: () => ({append(){},setAttribute(){}}), updateMergeSelection(){},
    state: {editing: {id:956}},
  });
  vm.runInContext(`let suggestionController,suggestionGeneration=0,suggestionPool=[],suggestionShown=0,suggestionHasMore=false,suggestedGroups=[]; const dismissedSuggestionIds=new Set();
    function appendSuggestionPage(){suggestionShown=Math.min(suggestionShown+25,suggestionPool.length);updateSuggestionProgress();}
    function updateSuggestionProgress(){$('moreSuggestions').hidden=suggestionShown>=suggestionPool.length&&!suggestionHasMore;}
    ` + flow, context);
  return {context,nodes,read:expression=>JSON.parse(vm.runInContext(`JSON.stringify(${expression})`,context))};
}
test('first results do not wait for 1000 matches; more preserves shown order and fetches once', async () => {
  const calls=[];
  const h=harness(async url=>{calls.push(url);return url.includes('preview=true')?{items:items(1,25),has_more:true}:{items:items(1,1000).reverse()};});
  await h.context.loadPersonSuggestions(h.context.state.editing);
  assert.equal(calls.length,1);assert.match(calls[0],/limit=25&preview=true/);
  assert.equal(h.read('suggestionShown'),25);assert.equal(h.nodes.get('moreSuggestions').hidden,false);
  await h.context.loadMorePersonSuggestions();
  assert.equal(calls.length,2);assert.match(calls[1],/limit=1000/);
  assert.deepEqual(h.read('suggestionPool.slice(0,25).map(p=>p.id)'),items(1,25).map(p=>p.id));
  assert.equal(h.read('new Set(suggestionPool.map(p=>p.id)).size'),1000);
  assert.equal(h.read('suggestionShown'),50);
  await h.context.loadMorePersonSuggestions();assert.equal(calls.length,2);assert.equal(h.read('suggestionShown'),75);
});
test('loading more cannot resurrect dismissed matches', async () => {
  const h=harness(async url=>url.includes('preview=true')?{items:items(1,25),has_more:true}:{items:items(1,1000)});
  await h.context.loadPersonSuggestions(h.context.state.editing);
  vm.runInContext('dismissedSuggestionIds.add(3);dismissedSuggestionIds.add(26)',h.context);
  await h.context.loadMorePersonSuggestions();
  assert.equal(h.read('suggestionPool.some(p=>p.id===3||p.id===26)'),false);
});
test('late results from another person cannot overwrite the current review', async () => {
  let resolveMore;
  const h=harness(async url=>url.includes('limit=1000')?new Promise(resolve=>{resolveMore=resolve;}):{items:items(url.includes('/956/')?1:201,25),has_more:true});
  await h.context.loadPersonSuggestions(h.context.state.editing);
  const pending=h.context.loadMorePersonSuggestions();
  h.context.state.editing={id:2};await h.context.loadPersonSuggestions(h.context.state.editing);
  resolveMore({items:items(1,1000)});await pending;
  assert.deepEqual(h.read('suggestionPool.map(p=>p.id)'),items(201,25).map(p=>p.id));
  assert.equal(h.nodes.get('moreSuggestions').disabled,false);
});
test('failed expansion keeps the visible matches and allows retry', async () => {
  let fail=true;
  const h=harness(async url=>{if(url.includes('preview=true'))return {items:items(1,25),has_more:true};if(fail)throw Error('Retry this request');return {items:items(1,1000)};});
  await h.context.loadPersonSuggestions(h.context.state.editing);await h.context.loadMorePersonSuggestions();
  assert.equal(h.read('suggestionPool.length'),25);assert.equal(h.nodes.get('moreSuggestions').disabled,false);
  assert.equal(h.nodes.get('editError').textContent,'Retry this request');
  fail=false;await h.context.loadMorePersonSuggestions();assert.equal(h.read('suggestionShown'),50);
});
