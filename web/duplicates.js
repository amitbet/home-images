"use strict";
const $=id=>document.getElementById(id);
const el=(tag,value)=>{const n=document.createElement(tag);n.textContent=value;return n;};
let offset=0,busy=false;
async function request(url,body){const r=await fetch(url,body?{method:"POST",headers:{"Content-Type":"application/json","X-Home-Images":"1"},body:JSON.stringify(body)}:{});const data=await r.json();if(!r.ok)throw Error(data.detail||"Request failed");return data;}
async function load(){if(busy)return;busy=true;$("duplicatesSpinner").hidden=false;$("status").textContent="Loading duplicate groups…";try{
 const data=await request("/api/duplicates?limit=20&offset="+offset);$("groups").replaceChildren();
 for(const group of data.items){const article=el("article","");article.append(el("h2",group.files.length+" file locations"));
 const keepLabel=el("label","Keep a copy: ");const select=document.createElement("select");select.setAttribute("aria-label","Copy to keep");for(const f of group.files)select.append(new Option(f.path,f.id));keepLabel.append(select);article.append(keepLabel);
 const buttons=[];
 for(const file of group.files){const row=el("div","");row.className="copy";const link=document.createElement("a");link.href="/?image="+encodeURIComponent(file.id);link.target="_blank";link.rel="noopener";const img=document.createElement("img");img.loading="lazy";img.src="/api/images/"+file.id+"/thumbnail";img.alt="Preview of "+file.path;link.append(img);const info=el("div","");info.className="path";info.append(el("div",file.path),el("p",file.size<1048576?(file.size/1024).toFixed(1)+" KB":(file.size/1048576).toFixed(2)+" MB"));const button=el("button","Move to Trash");buttons.push({button,file});
 button.onclick=async()=>{if(busy)return;const retained=group.files.find(f=>f.id===select.value);if(!confirm("Keep:\n"+retained.path+"\n\nMove this exact duplicate to Trash?\n"+file.path))return;busy=true;button.disabled=true;$("refresh").disabled=true;$("status").textContent="Verifying both originals and moving the selected copy to Trash…";try{await request("/api/duplicates/quarantine-copy",{keep:retained.id,remove:file.id});busy=false;await load();$("status").textContent="Copy moved to Trash. The retained original is still in the library.";}catch(e){$("status").textContent=e.message;}finally{busy=false;$("refresh").disabled=false;sync();}};
 row.append(link,info,button);article.append(row);}
 const sync=()=>{const keep=group.files.find(f=>f.id===select.value);for(const {button,file}of buttons){button.disabled=file.id===keep.id||file.file_hash!==keep.file_hash;button.title=file.id===keep.id?"This copy will be kept":file.file_hash!==keep.file_hash?"Metadata or file bytes differ; review this variant manually":"Move exact duplicate to Trash; you can restore it later";}};select.onchange=sync;sync();$("groups").append(article);
 }
 $("page").textContent=data.total?`${offset+1}–${offset+data.items.length} of ${data.total} groups`:"No duplicate groups";$("previous").disabled=offset===0;$("next").disabled=offset+data.items.length>=data.total;$("status").textContent=data.total+" groups with matching content. Exact copies are verified again before cleanup.";
 }catch(e){$("status").textContent=e.message;}finally{busy=false;$("duplicatesSpinner").hidden=true;}}
$("previous").onclick=()=>{offset=Math.max(0,offset-20);load();};$("next").onclick=()=>{offset+=20;load();};$("refresh").onclick=()=>load();if(new URLSearchParams(location.search).get("view")==="files")load();
