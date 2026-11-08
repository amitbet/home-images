const {test} = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const source = fs.readFileSync('web/app.js', 'utf8');

function fixture() {
  const requests = [], observed = new Set();
  let notify;
  const context = vm.createContext({state:{generation:1}, innerHeight:600, innerWidth:800,
    $:()=>({open:false}), AbortController, setTimeout:()=>0, clearTimeout(){},
    URL:{createObjectURL:()=> 'blob:test',revokeObjectURL(){}},
    IntersectionObserver:class {
      constructor(callback){notify=callback;}
      observe(img){observed.add(img);}
      unobserve(img){observed.delete(img);}
      disconnect(){observed.clear();}
    },
    fetch(url,options){
      let resolve;
      const promise = new Promise(r=>{resolve=r;});
      requests.push({url,options,finish:()=>resolve({ok:true,blob:async()=>({size:1})})});
      return promise;
    },
  });
  vm.runInContext(source.slice(source.indexOf('const clientThumbnails ='), source.indexOf('const fields =')),context);
  const image = (name,top) => ({dataset:{preview:name},isConnected:true,top,
    getBoundingClientRect(){return {top:this.top,bottom:this.top+100,left:0,right:100};}});
  return {context,requests,observed,image,enter:images=>notify(images.map(target=>({target,isIntersecting:true})))};
}
const settle = () => new Promise(resolve=>setImmediate(resolve));

test('visible thumbnails dispatch before offscreen entries in the same observer batch',()=>{
  const f=fixture();
  f.enter([f.image('below',650),f.image('visible',100),f.image('above',-120)]);
  assert.deepEqual(f.requests.map(r=>r.url),['visible']);
  assert.equal(f.requests[0].options.priority,'high');
});

test('offscreen prefetch leaves slots available for newly visible thumbnails',()=>{
  const f=fixture();
  f.enter([f.image('prefetch',650),f.image('prefetch2',700)]);
  assert.deepEqual(f.requests.map(r=>r.url),['prefetch']);
  assert.equal(f.requests[0].options.priority,'low');
  f.enter([f.image('visible1',100),f.image('visible2',200),f.image('visible3',300)]);
  assert.deepEqual(f.requests.map(r=>r.url),['prefetch','visible1','visible2','visible3']);
});

test('scrolling reprioritizes queued thumbnails and reobserves distant jobs',async()=>{
  const f=fixture();
  const images=Array.from({length:7},(_,i)=>f.image('image'+i,100));
  f.enter(images);
  assert.equal(f.requests.length,4);
  images[4].top=-1000; // No longer near the viewport.
  images[5].top=650;
  images[6].top=200;
  f.requests[0].finish(); await settle();
  assert.equal(f.requests[4].url,'image6');
  assert.equal(f.observed.has(images[4]),true);
  assert.equal(f.requests.some(r=>r.url==='image4'),false);
});
