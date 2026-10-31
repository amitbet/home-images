(()=>{"use strict";
const $=id=>document.getElementById(id), node=(tag,text,cls)=>{const n=document.createElement(tag);n.textContent=text;if(cls)n.className=cls;return n;};
const number=n=>Number(n||0).toLocaleString(), bytes=n=>n>=1073741824?(n/1073741824).toFixed(2)+" GB":n>=1048576?(n/1048576).toFixed(1)+" MB":(n/1024).toFixed(1)+" KB";
const view=new URLSearchParams(location.search).get("view")||"folders";
$("folderView").hidden=view!=="folders";$("filesView").hidden=view!=="files";$("trashView").hidden=view!=="trash";$(view==="files"?"fileTab":view==="trash"?"trashTab":"folderTab").setAttribute("aria-current","page");if(view!=="folders")return;
let offset=0,loading=false,plan=null;
async function request(url,body){const r=await fetch(url,body?{method:"POST",headers:{"Content-Type":"application/json","X-Home-Images":"1"},body:JSON.stringify(body)}:{});const data=await r.json();if(!r.ok)throw Error(data.detail||"Request failed");return data;}
function params(){return new URLSearchParams({scope:$("overlapScope").value.trim(),depth:$("overlapDepth").value,minimum:$("overlapMinimum").value,q:$("overlapQuery").value.trim(),full:String($("overlapFull").checked),offset:String(offset),limit:"25"});}
function setLoading(active){loading=active;$("overlapSpinner").hidden=!active;$("overlapCompare").disabled=active;$("refresh").disabled=active;$("overlapResults").hidden=active;$("overlapResults").setAttribute("aria-busy",String(active));}
async function load(force=false){if(loading)return;setLoading(true);$("overlapStatus").textContent="Finding directory overlap matches… Please wait while folder contents are compared.";$("overlapPrevious").disabled=true;$("overlapNext").disabled=true;try{
const data=await request("/api/duplicates/overlaps?"+params()+(force?"&refresh=1":""));$("overlapResults").replaceChildren();const table=document.createElement("table"),head=document.createElement("thead"),row=document.createElement("tr");for(const label of ["Folder A", "Folder B"])row.append(node("th",label));head.append(row);table.append(head);table.className="overlap-table";
for(const pair of data.items){
 const body=document.createElement("tbody");
 const a=pair.first,b=pair.second,summaryRow=node("tr",""),summary=node("td","","pair-summary");summary.colSpan=2;
 const metric=node("div","","overlap-metrics");
 const describe=d=>number(d.exact_files)+" exact copies / "+bytes(d.bytes);
 if(a.exact_files===b.exact_files&&a.bytes===b.bytes){metric.append(node("strong",describe(a)),node("span"," can be moved from either folder"));}
 else{metric.append(node("strong","Folder A: "+describe(a)),node("span"," in folder A"),node("strong","Folder B: "+describe(b)),node("span"," in folder B"));}
 const matching=node("p","","overlap-match");
 if(a.content_files===b.content_files)matching.textContent=number(a.content_files)+" matching images in each folder";
 else matching.textContent="Matching images: "+number(a.content_files)+" in folder A; "+number(b.content_files)+" in folder B";
 if(a.content_files>a.exact_files||b.content_files>b.exact_files)matching.append(node("span","Some matching images differ in file data or metadata and stay in place."));
 summary.append(metric,matching);if(a.content_files>a.exact_files||b.content_files>b.exact_files){const inspect=node("button","Inspect differences");inspect.onclick=()=>openVariants(a.source,b.source);summary.append(inspect);}summaryRow.append(summary);body.append(summaryRow);
 const tr=node("tr","");for(const direction of [a,b]){const cell=node("td","","folder"),button=node("button","Keep this folder");const other=direction===a?b:a;
 cell.append(node("div",direction.source),node("p",number(direction.source_files)+" photos in this tree","small"),node("p",(direction.source_files?Math.round(100*direction.exact_files/direction.source_files):0)+"% have exact copies in the other folder"+(direction.full?" - fully covered":""),"small"));
 button.disabled=!other.exact_files;button.onclick=()=>review(other);cell.append(button);tr.append(cell);}body.append(tr);table.append(body);
}const wrapper=node("div","","table-scroll overlap-wrapper");wrapper.append(table);$("overlapResults").append(wrapper);if(!data.total)$("overlapResults").append(node("p","No matching folder pairs at this depth and minimum. Try a lower minimum or choose a parent folder and a different depth."));$("overlapStatus").textContent=number(data.directories)+" folder trees compared · "+number(data.total)+" overlapping folder pairs. Choose which folder to keep.";$("overlapPage").textContent=data.total?`${number(offset+1)}–${number(offset+data.items.length)} of ${number(data.total)}`:"No results";$("overlapPrevious").disabled=offset===0;$("overlapNext").disabled=offset+data.items.length>=data.total;
}catch(e){$("overlapStatus").textContent=e.message;}finally{setLoading(false);}}
let variantA="",variantB="",variantOffset=0,variantSelections=new Map();
function updateVariantSelection(){const n=variantSelections.size;$("variantSelection").textContent=number(n)+" file"+(n===1?"":"s")+" selected to move to Trash.";$("variantApply").disabled=n===0;}
async function openVariants(a,b){variantA=a;variantB=b;variantOffset=0;variantSelections.clear();updateVariantSelection();$("variantDialog").showModal();await loadVariants();}
async function loadVariants(){
 $("variantStatus").textContent="Loading matching images...";$("variantPrevious").disabled=true;$("variantNext").disabled=true;$("variantResults").replaceChildren();
 try{const query=new URLSearchParams({a:variantA,b:variantB,offset:String(variantOffset)}),data=await request("/api/duplicates/variants?"+query);
  for(const pair of data.items){const article=node("article","","variant-pair"),files=node("div","","variant-files");
   for(const photo of [pair.first,pair.second]){const section=node("div",""),link=node("a","");link.href="/?image="+encodeURIComponent(photo.id);link.target="_blank";link.rel="noopener";const image=document.createElement("img");image.loading="lazy";image.src="/api/images/"+encodeURIComponent(photo.id)+"/thumbnail";image.alt="Preview of "+photo.path;link.append(image);section.append(link,node("p",photo.path,"path"),node("p",bytes(photo.size_bytes)+" | "+photo.width+" x "+photo.height+" | "+photo.format,"small"));files.append(section);}article.append(files);
   const a=pair.first,b=pair.second,key=a.id+":"+b.id,choice=node("fieldset","","variant-choice"),legend=node("legend","Choose what to keep");choice.append(legend);
   for(const [value,label] of [["none","Leave both unchanged"],["first","Keep folder A"],["second","Keep folder B"]]){const wrap=node("label",""),input=document.createElement("input");input.type="radio";input.name="choice-"+key;input.value=value;input.checked=(variantSelections.get(key)?.choice||"none")===value;input.onchange=()=>{if(value==="none")variantSelections.delete(key);else{const keep=value==="first"?a:b,remove=value==="first"?b:a;variantSelections.set(key,{choice:value,keep:keep.id,remove:remove.id,keep_path:keep.path,remove_path:remove.path,keep_hash:value==="first"?pair.first_hash:pair.second_hash,remove_hash:value==="first"?pair.second_hash:pair.first_hash});}updateVariantSelection();};wrap.append(input,document.createTextNode(label));choice.append(wrap);}article.append(choice);
   const fields=[["File size",a.size_bytes,b.size_bytes],["Format",a.format,b.format],["Width",a.width,b.width],["Height",a.height,b.height],["Taken",a.taken_at,b.taken_at],["Created",a.created_at,b.created_at],["Modified",a.modified_at,b.modified_at]];
   for(const key of new Set([...Object.keys(a.exif||{}),...Object.keys(b.exif||{})]))fields.push(["EXIF: "+key,(a.exif||{})[key],(b.exif||{})[key]]);
   const fmt=value=>value==null?"Not available":typeof value==="object"?JSON.stringify(value):String(value),diffs=fields.filter(([,x,y])=>fmt(x)!==fmt(y));
   if(diffs.length){const table=node("table","","variant-diffs"),head=node("tr","");for(const label of ["Different field","Folder A","Folder B"])head.append(node("th",label));const thead=node("thead","");thead.append(head);table.append(thead);const body=node("tbody","");for(const [label,x,y] of diffs){const row=node("tr","");row.append(node("th",label),node("td",fmt(x)),node("td",fmt(y)));body.append(row);}table.append(body);article.append(table);}else article.append(node("p","No differences found in the indexed metadata. The full files still differ.","small"));$("variantResults").append(article);
  }
  $("variantStatus").textContent=data.items.length?"Showing "+number(variantOffset+1)+"-"+number(variantOffset+data.items.length)+" matching file pairs.":"No differing file pairs are currently indexed for these folders.";$("variantPrevious").disabled=variantOffset===0;$("variantNext").disabled=!data.more;
 }catch(e){$("variantStatus").textContent=e.message;}
}
$("variantApply").onclick=async()=>{
 const items=[...variantSelections.values()].map(({choice,...item})=>item);if(!items.length)return;
 const removed=new Set(),kept=new Set(items.map(x=>x.keep));for(const item of items){if(removed.has(item.remove)||kept.has(item.remove)){$("variantStatus").textContent="A file is selected in conflicting pairs. Choose which copy to keep consistently before continuing.";return;}removed.add(item.remove);}
 if(!confirm("Move "+number(items.length)+" selected file(s) to Trash? The files have different metadata or file bytes. Their selected counterparts will be kept."))return;
 $("variantApply").disabled=true;$("variantStatus").textContent="Checking selected files...";
 try{const result=await request("/api/duplicates/variants-plan",{a:variantA,b:variantB,items});await request("/api/duplicates/batch-start",{id:result.id,action:"quarantine"});$("variantDialog").close();$("folderHistory").scrollIntoView({behavior:"smooth",block:"start"});history();}
 catch(e){$("variantStatus").textContent=e.message;updateVariantSelection();}
};
$("variantPrevious").onclick=()=>{variantOffset=Math.max(0,variantOffset-20);loadVariants();};$("variantNext").onclick=()=>{variantOffset+=20;loadVariants();};$("variantClose").onclick=()=>$("variantDialog").close();
async function review(pair){$("overlapStatus").textContent="Building a cleanup plan for the selected directory trees…";try{plan=await request("/api/duplicates/plan",{source:pair.source,keep:pair.keep});const summary=plan.summary;$("planPaths").textContent="Clean: "+summary.source+"\nKeep: "+summary.keep;$("planPaths").style.whiteSpace="pre-wrap";$("planSummary").replaceChildren();for(const [value,label]of [[number(plan.count),"exact copies to move"],[bytes(summary.bytes),"potential space after deleting Trash"],[number(summary.source_files-plan.count),"photos left in source tree"]]){const block=node("div","");block.append(node("strong",value),node("span",label));$("planSummary").append(block);}$("planPreserved").textContent=`Preserved in the source tree: ${number(summary.unique)} unmatched photos, ${number(summary.variants)} metadata variants, ${number(summary.pending_files)} awaiting hashes. Non-photo files are untouched.`;$("planSampleLabel").textContent="Preview "+number(plan.sample.length)+" of "+number(plan.count)+" matches";$("planFiles").replaceChildren();for(const item of plan.sample){const div=node("div","","plan-file");const a=node("a","");a.href="/?image="+encodeURIComponent(item.remove_id);a.target="_blank";a.rel="noopener";a.setAttribute("aria-label","Open photo: "+item.remove_path);const img=document.createElement("img");img.loading="lazy";img.src="/api/images/"+encodeURIComponent(item.remove_id)+"/thumbnail";img.alt="";a.append(img);div.append(a,node("span","Move: "+item.remove_path,"small"),node("span","Keep: "+item.keep_path,"small"));$("planFiles").append(div);}$("planError").textContent="";$("planStart").disabled=false;$("planStart").textContent="Move "+number(plan.count)+" copies to Trash";$("folderPlan").showModal();$("overlapStatus").textContent="Review the exact file count and preserved files before starting.";}catch(e){$("overlapStatus").textContent=e.message;}}
async function start(id,action){await request("/api/duplicates/batch-start",{id,action});await history();}
function jobControls(job,container){container.replaceChildren();const running=job.state==="running"||job.state==="cancelling";if(running){const cancel=node("button","Cancel remaining");cancel.onclick=async()=>{cancel.disabled=true;try{await request("/api/duplicates/batch-cancel",{id:job.id});await history();}catch(e){$("jobHistoryStatus").textContent=e.message;}};container.append(cancel);}else if((job.counts?.pending||job.counts?.error)&&job.state!=="preview"){const resume=node("button","Resume / retry skipped");resume.onclick=()=>start(job.id,"resume").catch(e=>$("jobHistoryStatus").textContent=e.message);container.append(resume);}}

let historyTimer=null,historyBusy=false;
const jobRows=new Map();
async function history(){
 if(historyBusy)return;historyBusy=true;clearTimeout(historyTimer);
 try{
  const data=await request("/api/duplicates/batches?view=tasks");
  if(!$("jobHistoryTable")){
   const wrap=node("div","","table-scroll"),table=node("table","");table.id="jobHistoryTable";
   const head=node("thead",""),tr=node("tr","");for(const label of ["Folders","Status","Progress","Actions"])tr.append(node("th",label));head.append(tr);table.append(head,node("tbody",""));wrap.append(table);$("jobHistory").replaceChildren(wrap);
  }
  const body=$("jobHistoryTable").querySelector("tbody"),seen=new Set();let position=0;
  for(const item of data.items){
   seen.add(item.id);let row=jobRows.get(item.id);
   if(!row){row=node("tr","");const paths=node("td","","path"),state=node("td",""),progressCell=node("td",""),actions=node("td","");const progress=node("progress","");progress.max=100;progress.style.width="100%";const count=node("div","","small");progressCell.append(progress,count);actions.className="job-buttons";row.append(paths,state,progressCell,actions);jobRows.set(item.id,row);}
   row.cells[0].textContent=item.summary.source+" -> keep "+item.summary.keep;
   row.cells[1].textContent=({quarantine:"Move to Trash",restore:"Restore",purge:"Permanently delete"}[item.action]||item.action)+" - "+item.state;
   const progress=row.cells[2].querySelector("progress");progress.value=100*(item.done+item.failed)/Math.max(1,item.total);progress.setAttribute("aria-label","Cleanup progress for "+item.summary.source);
   row.cells[2].querySelector("div").textContent=number(item.done)+" / "+number(item.total)+" completed - "+number(item.failed)+" skipped or failed - "+bytes(item.completed_bytes)+" processed";
   const actions=row.cells[3],key=[item.state,item.pending>0,item.failed>0,item.trash_count>0].join(":");
   if(actions.dataset.key!==key){actions.dataset.key=key;jobControls({...item,counts:{pending:item.pending,error:item.failed}},actions);
    if(item.failed){const errors=node("button","View skipped / failed");errors.onclick=async()=>{try{const detail=await request("/api/duplicates/batches/"+encodeURIComponent(item.id));const list=node("div","");for(const e of detail.errors)list.append(node("p",e.path+": "+e.message,"error"));errors.replaceWith(list);}catch(e){$("jobHistoryStatus").textContent=e.message;}};actions.append(errors);}}
   const at=body.children[position++];if(at!==row)body.insertBefore(row,at||null);
  }
  for(const [id,row] of jobRows)if(!seen.has(id)){row.remove();jobRows.delete(id);}
  if(!data.items.length){const row=node("tr","");row.id="noJobs";const cell=node("td","No active or unfinished cleanup jobs.");cell.colSpan=4;row.append(cell);body.replaceChildren(row);}else $("noJobs")?.remove();
 }catch(e){$("jobHistoryStatus").textContent=e.message;}
 finally{historyBusy=false;historyTimer=setTimeout(history,3000);}
}
$("planCancel").onclick=()=>$("folderPlan").close();$("planStart").onclick=async()=>{if(!plan)return;$("planStart").disabled=true;$("planStart").textContent="Starting cleanup...";$("planError").textContent="";try{await request("/api/duplicates/batch-start",{id:plan.id,action:"quarantine"});$("folderPlan").close();$("folderHistory").scrollIntoView({behavior:"smooth",block:"start"});history();}catch(e){$("planError").textContent=e.message;$("planStart").textContent="Move "+number(plan.count)+" copies to Trash";$("planStart").disabled=false;}};
$("overlapForm").onsubmit=e=>{e.preventDefault();if(loading)return;offset=0;load();};$("overlapPrevious").onclick=()=>{if(!loading){offset=Math.max(0,offset-25);load();}};$("overlapNext").onclick=()=>{if(!loading){offset+=25;load();}};$("refresh").onclick=()=>{load(true);history();};load();history();
})();
