(()=>{"use strict";
if(new URLSearchParams(location.search).get("view")!=="trash")return;
const $=id=>document.getElementById(id),node=(tag,text)=>{const n=document.createElement(tag);n.textContent=text;return n;};
let busy=false,last="",timer;
async function request(url,body){const r=await fetch(url,body?{method:"POST",headers:{"Content-Type":"application/json","X-Home-Images":"1"},body:JSON.stringify(body)}:{});const data=await r.json();if(!r.ok)throw Error(data.detail||"Request failed");return data;}
async function run(button,action,id,label,group){
 if(busy)return;
 if(action==="purge"&&!confirm("Permanently delete "+label+"? This cannot be undone."))return;
 if(action==="restore"&&!confirm("Restore "+label+" to the original location?"))return;
 busy=true;button.disabled=true;$("trashStatus").textContent=action==="purge"?"Starting permanent deletion...":"Starting restore...";
 try{await request(group?"/api/duplicates/batch-start":"/api/duplicates/"+action,group?{id,action}:{token:id});last="";$("trashStatus").textContent=group?"Started. Progress appears under Cleanup jobs on Folder overlaps.":"Done.";}
 catch(e){$("trashStatus").textContent=e.message;}
 finally{busy=false;button.disabled=false;load();}
}
async function load(){
 if(busy)return;clearTimeout(timer);
 try{
  const [groups,singles]=await Promise.all([request("/api/duplicates/batches?view=trash"),request("/api/duplicates/quarantine?ungrouped=1")]);
  const key=JSON.stringify([groups.items,singles.items]);if(key===last)return;last=key;
  const groupContainer=$("trashGroups"),singleContainer=$("trashSingles");groupContainer.replaceChildren();singleContainer.replaceChildren();
  for(const group of groups.items){
   const article=node("article",""),title=node("h3",group.summary.source),description=node("p",Number(group.trash_count).toLocaleString()+" files in Trash - kept "+group.summary.keep),controls=node("div","");controls.className="job-buttons";
   const restore=node("button","Restore group"),purge=node("button","Permanently delete group");purge.className="danger";
   const active=group.state==="running"||group.state==="cancelling";restore.disabled=purge.disabled=active;
   restore.onclick=()=>run(restore,"restore",group.id,group.trash_count+" files",true);purge.onclick=()=>run(purge,"purge",group.id,group.trash_count+" files",true);
   controls.append(restore,purge);article.append(title,description,controls);if(active)article.append(node("p","This group is still being moved to Trash."));groupContainer.append(article);
  }
  if(!groups.items.length)groupContainer.append(node("p","No groups in Trash."));
  for(const item of singles.items){const row=node("div","");row.className="quarantine";const path=node("span",item.path);path.className="path";const controls=node("div","");controls.className="job-buttons";const restore=node("button","Restore"),purge=node("button","Permanently delete");purge.className="danger";restore.onclick=()=>run(restore,"restore",item.token,item.path,false);purge.onclick=()=>run(purge,"purge",item.token,item.path,false);controls.append(restore,purge);row.append(path,controls);singleContainer.append(row);}
  if(!singles.items.length)singleContainer.append(node("p","No individual files in Trash."));
 }catch(e){$("trashStatus").textContent=e.message;}
 finally{timer=setTimeout(load,5000);}
}
$("refresh").onclick=()=>{last="";load();};load();
})();
