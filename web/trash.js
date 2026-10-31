(()=>{"use strict";
if(new URLSearchParams(location.search).get("view")!=="trash")return;
const $=id=>document.getElementById(id),node=(tag,text)=>{const n=document.createElement(tag);n.textContent=text;return n;};
let busy=false,lastGroups="",lastSingles="",timer;
async function request(url,body){const r=await fetch(url,body?{method:"POST",headers:{"Content-Type":"application/json","X-Home-Images":"1"},body:JSON.stringify(body)}:{});const data=await r.json();if(!r.ok)throw Error(data.detail||"Request failed");return data;}
async function run(button,action,id,label,group){
 if(busy)return;
 if(action==="purge"&&!confirm("Permanently delete "+label+"? This cannot be undone."))return;
 if(action==="restore"&&!confirm("Restore "+label+" to the original location?"))return;
 busy=true;button.disabled=true;$("trashStatus").textContent=action==="purge"?"Starting permanent deletion...":"Starting restore...";
 try{await request(group?"/api/duplicates/batch-start":"/api/duplicates/"+action,group?{id,action}:{token:id});lastGroups=lastSingles="";$("trashStatus").textContent=group?"Started. Progress appears under Cleanup jobs on Folder overlaps.":"Done.";}
 catch(e){$("trashStatus").textContent=e.message;}
 finally{busy=false;button.disabled=false;load();}
}
async function loadGroups(){
 const data=await request("/api/duplicates/batches?view=trash"),key=JSON.stringify(data.items);if(key===lastGroups)return;lastGroups=key;
 const container=$("trashGroups");container.replaceChildren();
 for(const group of data.items){
  const article=node("article",""),title=node("h3",group.summary.source),description=node("p",Number(group.trash_count).toLocaleString()+" files in Trash - kept "+group.summary.keep),controls=node("div","");controls.className="job-buttons";
  const restore=node("button","Restore group"),purge=node("button","Permanently delete group");purge.className="danger";
  const active=group.state==="running"||group.state==="cancelling";restore.disabled=purge.disabled=active;
  restore.onclick=()=>run(restore,"restore",group.id,group.trash_count+" files",true);purge.onclick=()=>run(purge,"purge",group.id,group.trash_count+" files",true);
  controls.append(restore,purge);article.append(title,description,controls);if(active)article.append(node("p","This group is still being moved to Trash."));container.append(article);
 }
 if(!data.items.length)container.append(node("p","No groups in Trash."));
}
async function loadSingles(){
 const data=await request("/api/duplicates/quarantine?ungrouped=1"),key=JSON.stringify(data.items);if(key===lastSingles)return;lastSingles=key;
 const container=$("trashSingles");container.replaceChildren();
 for(const item of data.items){const row=node("div","");row.className="quarantine";const path=node("span",item.path);path.className="path";const controls=node("div","");controls.className="job-buttons";const restore=node("button","Restore"),purge=node("button","Permanently delete");purge.className="danger";restore.onclick=()=>run(restore,"restore",item.token,item.path,false);purge.onclick=()=>run(purge,"purge",item.token,item.path,false);controls.append(restore,purge);row.append(path,controls);container.append(row);}
 if(!data.items.length)container.append(node("p","No individual files in Trash."));
}
async function load(){
 if(busy)return;clearTimeout(timer);
 const results=await Promise.allSettled([loadGroups(),loadSingles()]);
 const failure=results.find(result=>result.status==="rejected");if(failure)$("trashStatus").textContent=failure.reason.message;
 timer=setTimeout(load,5000);
}
$("refresh").onclick=()=>{lastGroups=lastSingles="";load();};load();
})();
