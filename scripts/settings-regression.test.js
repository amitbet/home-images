const test = require('node:test');
const assert = require('node:assert/strict');
const vm = require('node:vm');
const fs = require('node:fs');
test('settings navigation and theme use the shared preference', () => {
  const ids=['phonePanel','appearancePanel','settingsPhone','settingsAppearance','themeLight','themeDark'];
  const elements=Object.fromEntries(ids.map(id=>[id,{hidden:false,attrs:{},setAttribute(k,v){this.attrs[k]=v},removeAttribute(k){delete this.attrs[k]}}]));
  const store={}; let url='';
  const context={document:{documentElement:{dataset:{theme:'dark'}},getElementById:id=>elements[id]},localStorage:{setItem(k,v){store[k]=v}},history:{replaceState(_a,_b,v){url=v}},window:{addEventListener(){}},location:{search:''},URLSearchParams};
  vm.runInNewContext(fs.readFileSync('web/settings.js','utf8'),context);
  assert.equal(elements.phonePanel.hidden,false);assert.equal(elements.appearancePanel.hidden,true);assert.equal(elements.themeDark.attrs['aria-pressed'],'true');
  elements.settingsAppearance.onclick();assert.equal(elements.phonePanel.hidden,true);assert.equal(elements.appearancePanel.hidden,false);assert.equal(url,'/settings?tab=appearance');
  elements.themeLight.onclick();assert.equal(store['homeImages.theme'],'light');assert.equal(context.document.documentElement.dataset.theme,'light');assert.equal(elements.themeLight.attrs['aria-pressed'],'true');
  elements.settingsPhone.onclick();assert.equal(url,'/settings');assert.equal(elements.phonePanel.hidden,false);
});
