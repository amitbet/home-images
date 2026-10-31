const test = require('node:test');
const assert = require('node:assert/strict');
const diff = require('../web/metadata-diff.js');
test('compares nested EXIF objects by field and ignores key order',()=>{
 const a={Photo:{Lens:'wide',ISO:100,Date:'2009',Nested:{x:1,y:3}},GPS:{coords:[1,2]}};
 const b={GPS:{coords:[1,3]},Photo:{Nested:{y:3,x:2},Date:'2009',ISO:100,Lens:'tele'}};
 assert.deepEqual(diff(a,b,'EXIF'),[['EXIF.GPS.coords[1]',2,3],['EXIF.Photo.Lens','wide','tele'],['EXIF.Photo.Nested.x',1,2]]);
});
test('distinguishes missing fields, nulls and nested additions',()=>{
 assert.deepEqual(diff({Photo:{old:null}},{Photo:{new:{field:1},old:''}},'EXIF'),[['EXIF.Photo.new.field',undefined,1],['EXIF.Photo.old',null,'']]);
 assert.deepEqual(diff({x:[1,2]},{x:[1,2]}),[]);
});
