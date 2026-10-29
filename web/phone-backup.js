"use strict";
const el = (id) => document.getElementById(id);
let pairToken = location.hash.slice(1);
// Keep a setup link usable on reload in this tab without leaving it in the URL.
try {
  if (pairToken && location.pathname === "/phone-setup") sessionStorage.setItem("homeImages.phonePair", pairToken);
  else if (location.pathname === "/phone-setup") pairToken = sessionStorage.getItem("homeImages.phonePair") || "";
} catch {}
if (pairToken) history.replaceState(null, "", location.pathname);
if (location.pathname === "/settings") {
  el("pageTitle").textContent = "Settings";
  document.title = "Settings - Home Images";
}
const message = (error) => {
  el("message").textContent = error?.message || String(error || "");
};

async function api(path, options = {}, token = "") {
  const response = await fetch("/api/phone-backup/" + path, {
    ...options,
    cache: "no-store",
    headers: {
      Authorization: "Bearer " + token,
      "Content-Type": "application/json",
      "X-Home-Images": "1",
      ...options.headers,
    },
  });
  if (!response.ok) throw Error(await response.text());
  return response.status === 204 ? null : response.json();
}

async function run(button, action) {
  button.disabled = true;
  message("");
  try {
    await action();
  } catch (error) {
    message(error);
  } finally {
    button.disabled = false;
  }
}

async function refresh() {
  const data = await api("phones");
  el("phones").replaceChildren();
  for (const phone of data.phones) {
    const row = document.createElement("div");
    row.className = "phone";
    const name = document.createElement("strong");
    name.textContent = phone.name;
    row.append(name);
    const actions = [
      ["New setup link", () => pair(phone.name, phone.id)],
      ["Revoke access", async () => {
        await api("phones/" + phone.id, { method: "DELETE" });
        el("pairing").hidden = true;
        await refresh();
      }],
    ];
    for (const [label, action] of actions) {
      const button = document.createElement("button");
      button.textContent = label;
      button.onclick = () => run(button, action);
      row.append(button);
    }
    el("phones").append(row);
  }
  el("manage").hidden = false;
}

async function pair(name, id = "") {
  const data = await api("phones", {
    method: "POST",
    body: JSON.stringify({ name, id }),
  });
  el("qr").src = data.qr;
  el("pairLink").href = data.url;
  el("pairing").hidden = false;
  el("pairExpires").textContent = "Setup link expires at " + new Date(data.expires).toLocaleTimeString() + ".";
  await refresh();
  el("pairing").scrollIntoView({ behavior: "smooth", block: "start" });
}

el("add").onsubmit = (event) => {
  event.preventDefault();
  run(event.submitter, () => pair(el("name").value));
};

async function copySetting(value) {
  if (navigator.clipboard && isSecureContext) {
    await navigator.clipboard.writeText(value);
    return;
  }
  // The home LAN setup page may use HTTP, where Clipboard API is unavailable.
  const input = document.createElement("textarea");
  input.value = value;
  document.body.append(input);
  input.select();
  const copied = document.execCommand("copy");
  input.remove();
  if (!copied) throw Error("Select and copy the value manually.");
}

async function setup() {
  el("admin").hidden = true;
  el("setup").hidden = false;
  const data = await api("pair", {}, pairToken);
  el("phoneName").textContent = "Connect " + data.name;
  el("setupQr").src = data.qr;
  el("setupLink").href = data.url;
  el("setupExpires").textContent = "Private setup link expires at " + new Date(data.expires).toLocaleTimeString() + ".";
  el("setupShare").hidden = false;
  el("download").disabled = false;
  const fields = [
    ["Protocol", null], ["Server", "host"], ["Port", "port"],
    ["Username", "username"], ["Password", "password"],
    ["Destination folder", "path"], ["Server fingerprint", "fingerprint"],
  ];
  for (const [label, key] of fields) {
    const value = String(key ? data[key] : "SFTP");
    const row = document.createElement("div");
    row.className = "setting";
    const heading = document.createElement("strong");
    heading.textContent = label;
    const code = document.createElement("code");
    code.textContent = value;
    const button = document.createElement("button");
    button.textContent = "Copy";
    button.onclick = () => run(button, async () => {
      await copySetting(value);
      button.textContent = "Copied";
    });
    row.append(heading, code, button);
    el("settings").append(row);
  }
}

el("download").onclick = () => run(el("download"), async () => {
  const response = await fetch("/api/phone-backup/settings", {
    headers: { Authorization: "Bearer " + pairToken },
    cache: "no-store",
  });
  if (!response.ok) throw Error(await response.text());
  const url = URL.createObjectURL(await response.blob());
  const link = document.createElement("a");
  link.href = url;
  link.download = "HomeImages.photosync";
  link.click();
  setTimeout(() => URL.revokeObjectURL(url), 10000);
});

if (location.pathname === "/phone-setup") {
  if (pairToken) setup().catch((error) => {
    try { sessionStorage.removeItem("homeImages.phonePair"); } catch {}
    message(error);
    const link = document.createElement("a");
    link.href = "/settings";
    link.textContent = "Open Settings to create a fresh QR code";
    el("message").append(document.createElement("br"), link);
  });
  else {
    el("admin").hidden = true;
    message("Scan a fresh QR code from the Phone backup page.");
  }
}

if (location.pathname !== "/phone-setup") refresh().catch(message);
