const metadataDifferences = (()=>{
 const isObject=value=>value!==null&&typeof value==="object";
 function walk(left,right,path,result){
  if(Object.is(left,right))return;
  if((isObject(left)&&right===undefined)||(left===undefined&&isObject(right))){
   const value=left===undefined?right:left,keys=Object.keys(value).sort();
   if(keys.length){for(const key of keys){const next=Array.isArray(value)?path+"["+key+"]":path?path+"."+key:key;walk(left===undefined?undefined:left[key],right===undefined?undefined:right[key],next,result);}return;}
  }
  if(isObject(left)&&isObject(right)&&Array.isArray(left)===Array.isArray(right)){
   const keys=Array.from(new Set([...Object.keys(left),...Object.keys(right)])).sort();
   for(const key of keys){const next=Array.isArray(left)?path+"["+key+"]":path?path+"."+key:key;walk(left[key],right[key],next,result);}
   return;
  }
  result.push([path,left,right]);
 }
 return (left,right,prefix="")=>{const result=[];walk(left,right,prefix,result);return result;};
})();
if(typeof module!=="undefined"&&module.exports)module.exports=metadataDifferences;
