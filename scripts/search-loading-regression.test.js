const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const vm=require('node:vm');
const source=fs.readFileSync('web/app.js','utf8');
const settle=()=>new Promise(resolve=>setImmediate(resolve));
function fixture(){
  const nodes={};
  for(const id of ['query','mode','resultsTitle','resultsCount','gallery','people','albums','empty','photoSort','sortDirection']){
    const classes=new Set();
    nodes[id]={value:'',textContent:'',classes,classList:{add:v=>classes.add(v),remove:v=>classes.delete(v)},replaceChildren(){},setAttribute(){}};
  }
  nodes.query.value='tree';nodes.mode.value='semantic';nodes.photoSort.value='taken_at';nodes.sortDirection.value='desc';
  nodes.photoSort.selectedOptions=[{textContent:'Time taken'}];
  let resolveSearch,rejectSearch,resolveDetails,paint;
  const search=new Promise((resolve,reject)=>{resolveSearch=resolve;rejectSearch=reject;});
  const details=new Promise(resolve=>{resolveDetails=resolve;});
  const context=vm.createContext({state:{view:'photos',generation:0,selected:new Set()},AbortController,URLSearchParams,
    $:id=>nodes[id],resetThumbnailRequests(){},selectionBar(){},notice(){},viewChrome(){},filterChips(){},syncURL(){},
    params:()=>new URLSearchParams(),gridPage:()=>search,api:()=>details,requestAnimationFrame:fn=>{paint=fn;},
    renderPhotos(){},loadThumbnailFaces(){},number:String,pagination(){}});
  vm.runInContext(source.slice(source.indexOf('let searchController;'),source.indexOf('function renderPhotos(')),context);
  return {nodes,context,resolveSearch,rejectSearch,resolveDetails,paint:()=>paint()};
}
test('loading spinner follows actual search, details and grid preparation stages',async()=>{
  const f=fixture(),done=f.context.load();
  assert.match(f.nodes.resultsCount.textContent,/Encoding text and searching/);
  assert.equal(f.nodes.resultsCount.classes.has('search-loading'),true);
  f.resolveSearch({items:[{id:'a'}],total:1,semantic:true,indexed_candidates:1});await settle();
  assert.equal(f.nodes.resultsCount.textContent,'Loading photo details…');
  f.resolveDetails({});await settle();
  assert.equal(f.nodes.resultsCount.textContent,'Preparing photo grid…');
  f.paint();assert.equal(await done,true);
  assert.equal(f.nodes.resultsCount.classes.has('search-loading'),false);
  assert.equal(f.nodes.resultsCount.textContent,'1 indexed candidates');
});
test('failed search removes its spinner and shows the failure status',async()=>{
  const f=fixture(),done=f.context.load();
  f.rejectSearch(Error('Unavailable'));assert.equal(await done,false);
  assert.equal(f.nodes.resultsCount.classes.has('search-loading'),false);
  assert.equal(f.nodes.resultsCount.textContent,'Search unavailable');
});
