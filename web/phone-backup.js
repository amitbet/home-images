"use strict";
const el = id => document.getElementById(id);
let current = null, loading = false, busy = false;
async function api(action, body) {
  const response = await fetch("/api/phone-backup/" + action, {
    method: body === undefined ? "GET" : "POST", cache: "no-store",
    headers: {"X-Home-Images":"1", "Content-Type":"application/json"},
    body: body === undefined ? undefined : JSON.stringify(body)
  });
  const data = await response.json();
  if (!response.ok) throw Error(data.detail || "Request failed");
  return data;
}
function node(tag, text, className) {
  const n = document.createElement(tag); if (text !== undefined) n.textContent = text;
  if (className) n.className = className; return n;
}
function button(text, action, className) {
  const n = node("button", text, className); n.type = "button";
  n.addEventListener("click", () => run(action)); return n;
}
async function run(action) {
  if (busy) return; busy = true; el("message").textContent = "";
  document.querySelectorAll("button").forEach(n => n.disabled = true);
  try { await action(); } catch (error) { el("message").textContent = error.message; }
  finally { busy = false; document.querySelectorAll("button").forEach(n => n.disabled = false); await refresh(); }
}
function bytes(value) {
  return value >= 1024 ** 3 ? (value / 1024 ** 3).toFixed(1) + " GB" : (value / 1024 ** 2).toFixed(1) + " MB";
}
function showRequests(data) {
  // Preserve typed names while polling. Rebuild only when the actual offers change.
  const signature = JSON.stringify([data.pending_devices, data.pending_folders]);
  if (el("requests").dataset.signature === signature) return;
  el("requests").dataset.signature = signature;
  el("requests").replaceChildren();
  for (const [id, device] of Object.entries(data.pending_devices || {})) {
    const row = node("div", undefined, "request");
    row.append(node("strong", device.name || "New Android phone"), node("p", "Check this ID matches your phone before approving."), node("code", id));
    const name = node("input"); name.value = device.name || "My Android"; name.maxLength = 80; name.setAttribute("aria-label", "Phone name for " + id); name.autocomplete = "off";
    row.append(name, button("Approve phone", () => api("accept-phone", {device:id, name:name.value}), "primary"));
    el("requests").append(row);
  }
  for (const [id, folder] of Object.entries(data.pending_folders || {})) {
    for (const [deviceID, offer] of Object.entries(folder.offeredBy || {})) {
      const phone = data.phones.find(p => p.id === deviceID);
      const row = node("div", undefined, "request");
      row.append(node("strong", offer.label || id), node("p", "Shared by " + (phone?.name || "your phone")));
      if (offer.receiveEncrypted || offer.remoteEncrypted) row.append(node("p", "Remove the folder’s encryption password in the phone app, then share it again."));
      else row.append(button("Back up this folder", () => api("accept-folder", {device:deviceID, folder:id}), "primary"));
      el("requests").append(row);
    }
  }
  if (!el("requests").children.length) el("requests").append(node("p", data.ready ? "No requests yet. Add Home Images in your phone app, then share your Camera folder." : "Enable the receiver to start pairing.", "muted"));
}
function showPhones(data) {
  el("phones").replaceChildren();
  for (const phone of data.phones || []) {
    const row = node("div", undefined, "phone"), heading = node("div", undefined, "phone-heading");
    heading.append(node("strong", phone.name), node("span", phone.connected ? "Connected" : "Offline", "pill")); row.append(heading);
    row.append(node("p", phone.imported + " files archived · " + (phone.last_backup ? "Last backup " + new Date(phone.last_backup).toLocaleString() : "Waiting for first backup")));
    row.append(node("p", "Library folder: " + phone.directory));
    if (!phone.folders.length) row.append(node("p", "Next: share your Camera folder with Home Images in Syncthing-Fork."));
    for (const folder of phone.folders) {
      const line = node("div", undefined, "folder");
      const state = !data.ready ? "Receiver stopped" : folder.errors ? "Transfer error — check the phone app" : folder.need_bytes > 0 ? bytes(folder.need_bytes) + " remaining" : folder.state === "idle" ? "Synced · archive checked every 5 seconds" : (folder.state || "Waiting");
      line.append(node("span", (folder.label || folder.id) + " · " + state));
      if (folder.need_bytes > 0 && folder.global_bytes > 0) {const progress = node("progress"); progress.max = folder.global_bytes; progress.value = Math.max(0, folder.global_bytes - folder.need_bytes); progress.setAttribute("aria-label", "Transfer progress for " + folder.label); line.append(progress);}
      row.append(line);
    }
    row.append(button("Remove phone", async () => {if (confirm("Remove " + phone.name + "? Archived photos will be kept.")) await api("remove-phone", {device:phone.id});}, "danger"));
    el("phones").append(row);
  }
  if (!el("phones").children.length) el("phones").append(node("p", "No phones connected yet.", "muted"));
}
async function refresh() {
  if (loading || busy) return; loading = true;
  try {
    const data = await api("status"); current = data;
    el("dot").className = "dot " + data.status;
    el("receiverState").textContent = data.ready ? "Receiver is ready" : data.enabled ? "Starting your receiver…" : "Phone backup is off";
    el("receiverDetail").textContent = data.ready ? "Managed by Home Images · Syncthing " + data.version + " · Transfer port " + data.port : data.enabled ? "The first start downloads and verifies Syncthing. You can leave this page open." : "Enable to install and configure a private Syncthing receiver.";
    el("enable").textContent = data.enabled ? "Disable phone backup" : "Enable phone backup"; el("enable").disabled = false;
    el("receiverError").hidden = !data.last_error; el("receiverError").textContent = data.last_error ? data.last_error + ". The receiver retries automatically. For connection problems, allow its Syncthing executable through your local-network firewall." : "";
    el("qr").hidden = !data.ready; el("qrPlaceholder").hidden = data.ready; el("manual").hidden = !data.ready;
    el("qrPlaceholder").textContent = data.enabled ? "Preparing your receiver…" : "Enable phone backup to create your receiver.";
    if (data.ready && el("qr").dataset.device !== data.device_id) {el("qr").src = "/api/phone-backup/qr?v=" + encodeURIComponent(data.device_id); el("qr").dataset.device = data.device_id;}
    el("deviceID").textContent = data.device_id;
    showRequests(data); showPhones(data);
  } catch (error) { el("message").textContent = error.message; }
  finally { loading = false; }
}
el("enable").addEventListener("click", () => run(() => api("enable", {enabled:!current?.enabled})));
el("refresh").addEventListener("click", () => refresh());
el("copyID").addEventListener("click", () => run(async () => {
  // The Clipboard API is unavailable on ordinary HTTP home-network addresses.
  let copied = false;
  if (navigator.clipboard?.writeText) {
    try {await navigator.clipboard.writeText(current.device_id); copied = true;} catch {}
  }
  if (!copied) {
    const field = node("textarea"); field.value = current.device_id; field.readOnly = true;
    field.style.cssText = "position:fixed;opacity:0"; document.body.append(field); field.select();
    copied = document.execCommand("copy"); field.remove();
  }
  if (!copied) throw Error("Select the receiver ID above and copy it manually.");
  el("message").textContent = "Receiver ID copied.";
}));
refresh();
setInterval(() => {if (!document.hidden) refresh();}, 5000);
