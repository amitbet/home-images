(()=>{"use strict";
const $=id=>document.getElementById(id);
function show(name){const phone=name==="phone";$("phonePanel").hidden=!phone;$("appearancePanel").hidden=phone;for(const [id,on] of [["settingsPhone",phone],["settingsAppearance",!phone]]){if(on)$(id).setAttribute("aria-current","page");else $(id).removeAttribute("aria-current");}history.replaceState(null,"",phone?"/settings":"/settings?tab=appearance");}
function theme(value){document.documentElement.dataset.theme=value;for(const v of ["light","dark"])$(v==="light"?"themeLight":"themeDark").setAttribute("aria-pressed",String(v===value));}
for(const value of ["light","dark"])$(value==="light"?"themeLight":"themeDark").onclick=()=>{try{localStorage.setItem("homeImages.theme",value);}catch(e){}theme(value);};
$("settingsPhone").onclick=()=>show("phone");$("settingsAppearance").onclick=()=>show("appearance");
window.addEventListener("storage",event=>{if(event.key==="homeImages.theme"&&(event.newValue==="dark"||event.newValue==="light"))theme(event.newValue);});
theme(document.documentElement.dataset.theme);show(new URLSearchParams(location.search).get("tab")==="appearance"?"appearance":"phone");
})();
