"use strict";
const $ = (id) => document.getElementById(id);
const state = {
  view: "photos",
  person: null,
  album: null,
  items: [],
  people: [],
  albums: [],
  annotations: {},
  offset: 0,
  total: 0,
  index: 0,
  editing: null,
  playing: null,
  generation: 0,
  viewerGeneration: 0,
  status: null,
  selected: new Set(),
  shuffle: false,
  bag: [],
  shuffleStarted: false,
  playbackPhoto: null,
  playbackHistory: [],
  busy: false,
  savedName: "",
  semantic: false,
  dirty: false,
};
const number = (n) => Number(n || 0).toLocaleString();
const text = (tag, value = "", className = "") => {
  const e = document.createElement(tag);
  e.textContent = value;
  if (className) e.className = className;
  return e;
};
const photoViews = () => !["people", "albums"].includes(state.view);
const normalClick = (e) => !e || (e.button === 0 && !e.ctrlKey && !e.metaKey && !e.shiftKey && !e.altKey);
const thumbnailObserver = typeof IntersectionObserver === "function"
  ? new IntersectionObserver((entries) => {
      for (const entry of entries) if (entry.isIntersecting) {
        const img = entry.target;
        img.src = img.dataset.preview;
        thumbnailObserver.unobserve(img);
      }
    }, { rootMargin: "300px" })
  : null;
function previewImage(img, url) {
  img.dataset.preview = url;
  if (thumbnailObserver) thumbnailObserver.observe(img);
  else img.src = url;
}
const fields = {
  directory: "directory",
  from: "dateFrom",
  to: "dateTo",
  place: "place",
  tag: "tag",
};
function date(photo) {
  const raw = photo.taken_at || photo.created_at || "";
  return raw ? raw.slice(0, 10).replace(/:/g, "-") : "";
}
async function api(path, body, signal) {
  const response = await fetch(
    path,
    body === undefined
      ? { signal }
      : {
          method: "POST",
          headers: { "Content-Type": "application/json", "X-Home-Images": "1" },
          body: JSON.stringify(body),
          signal,
        },
  );
  const data = await response.json();
  if (!response.ok) throw Error(data.detail || "Request failed");
  return data;
}
function notice(message, error = false) {
  $("notice").hidden = !message;
  $("notice").textContent = message;
  $("notice").classList.toggle("error", error);
}
let toastTimer;
function toast(message) {
  $("toast").textContent = message;
  $("toast").hidden = false;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => ($("toast").hidden = true), 4200);
}
function empty(title, description) {
  $("empty").hidden = false;
  $("emptyTitle").textContent = title;
  $("emptyText").textContent = description;
}
function modal(id) {
  const d = $(id);
  if (!d.open) {
    if (d.showModal) d.showModal();
    else d.setAttribute("open", "");
  }
}
function closeModal(id) {
  const d = $(id);
  if (d.close) d.close();
  else d.removeAttribute("open");
}
function params() {
  const p = new URLSearchParams({
    mode: $("mode").value,
    q: $("query").value.trim(),
  });
  for (const [key, id] of Object.entries(fields))
    if ($(id).value.trim()) p.set(key, $(id).value.trim());
  if (state.person) p.set("person", state.person.id);
  if (state.album) p.set("album", state.album.id);
  if (state.view === "favorites") p.set("favorite", "true");
  if (state.view === "hidden") p.set("hidden", "only");
  if (state.view === "memories") p.set("on_this_day", "true");
  return p;
}
function syncURL() {
  const p = params();
  p.set("view", state.view);
  p.set("offset", state.offset);
  history.replaceState(null, "", "/?" + p);
}
function resetInputs() {
  for (const id of Object.values(fields)) $(id).value = "";
  $("query").value = "";
  state.person = null;
  state.album = null;
  state.savedName = "";
}
function restoreQuery(query) {
  resetInputs();
  const p =
    query instanceof URLSearchParams ? query : new URLSearchParams(query);
  $("query").value = p.get("q") || "";
  $("mode").value = p.get("mode") === "metadata" ? "metadata" : "semantic";
  for (const [key, id] of Object.entries(fields))
    $(id).value = p.get(key) || "";
  if (p.get("person")) state.person = { id: Number(p.get("person")), name: "" };
  if (p.get("album"))
    state.album = { id: Number(p.get("album")), name: "Album" };
  state.view =
    p.get("favorite") === "true"
      ? "favorites"
      : p.get("hidden") === "only"
        ? "hidden"
        : p.get("on_this_day") === "true"
          ? "memories"
          : "photos";
}
const viewCopy = {
  photos: [
    "A lifetime of little moments.",
    "Find a familiar face. Rediscover a favorite place.",
    "All photos",
  ],
  favorites: [
    "The ones you come back to.",
    "A collection of your favorite moments.",
    "Favorites",
  ],
  people: [
    "The people in your story.",
    "Familiar faces, brought together. Give each group a name.",
    "People",
  ],
  albums: [
    "Every chapter has a home.",
    "Bring photos together without moving a single file.",
    "Albums",
  ],
  memories: [
    "A familiar day, another year.",
    "Photos taken on this date, across the years.",
    "On this day",
  ],
  hidden: [
    "A little space off-screen.",
    "Hidden photos stay here, safely out of your slideshow.",
    "Hidden photos",
  ],
};
function viewChrome() {
  const v = viewCopy[state.view] || viewCopy.photos;
  document.querySelectorAll("[data-view]").forEach((e) => {
    e.classList.toggle("active", e.dataset.view === state.view);
    if (e.dataset.view === state.view) e.setAttribute("aria-current", "page");
    else e.removeAttribute("aria-current");
  });
  $("pageTitle").textContent = v[0];
  $("pageSubtitle").textContent = v[1];
  $("resultsTitle").textContent = state.savedName || state.album?.name || v[2];
  $("query").placeholder =
    state.view === "people"
      ? "Find a person by name…"
      : "Try “family at the beach” or “a dog in the garden”…";
  $("searchForm").hidden = state.view === "albums";
  $("mode").hidden = state.view === "people";
  $("suggestions").hidden = !photoViews();
  $("slideshow").hidden = !photoViews();
  $("filtersToggle").hidden = !photoViews();
  $("newAlbum").hidden = state.view !== "albums";
  if ($("playbackLinks")) $("playbackLinks").hidden = !photoViews();
  if (!photoViews()) {
    $("filters").hidden = true;
    $("filtersToggle").setAttribute("aria-expanded", "false");
  }
  $("folderList").hidden = true;
  $("gallery").hidden = !photoViews();
  $("people").hidden = state.view !== "people";
  $("albums").hidden = state.view !== "albums";
  $("clearPerson").hidden = !state.person || !photoViews();
  if (state.person)
    $("clearPerson").textContent =
      (state.person.name || "Person " + state.person.id) + " ×";
  $("hideSelected").textContent =
    state.view === "hidden" ? "Restore to frame" : "Hide from frame";
}
function setView(view) {
  stopPlay();
  resetInputs();
  state.view = view;
  state.offset = 0;
  state.selected.clear();
  viewChrome();
  load();
}
function pagination() {
  const limit = state.view === "people" ? 48 : 60;
  const cap = state.semantic ? Math.min(state.total, 1200) : state.total;
  $("previous").hidden = state.offset === 0 || state.view === "albums";
  $("next").hidden =
    state.view === "albums" ||
    state.offset + state.items.length >= cap ||
    !state.items.length ||
    (state.semantic && state.offset + limit > 1000);
  $("pageLabel").textContent =
    state.view !== "albums" && state.total
      ? `${number(state.offset + 1)}–${number(state.offset + state.items.length)} of ${number(state.total)}`
      : "";
}
function filterChips() {
  const box = $("activeFilters");
  box.replaceChildren();
  if (!photoViews()) return;
  const chips = [];
  if (state.album)
    chips.push({
      label: "Album: " + state.album.name,
      clear: () => (state.album = null),
    });
  for (const [key, id] of Object.entries(fields))
    if ($(id).value)
      chips.push({
        label:
          (key === "directory"
            ? "Folder"
            : key === "from"
              ? "From"
              : key === "to"
                ? "Until"
                : key === "place"
                  ? "Place"
                  : "Tag") +
          ": " +
          $(id).value,
        clear: () => ($(id).value = ""),
      });
  for (const c of chips) {
    const b = text("button", c.label + " ×", "filter-chip");
    b.onclick = () => {
      c.clear();
      state.savedName = "";
      state.offset = 0;
      load();
    };
    box.append(b);
  }
}
let searchController;
let facesController;
let playbackController;
async function load() {
  if (searchController) searchController.abort();
  searchController = typeof AbortController === "function" ? new AbortController() : null;
  const signal = searchController?.signal;
  const gen = ++state.generation;
  state.selected.clear();
  selectionBar();
  notice("");
  $("empty").hidden = true;
  $("gallery").classList.add("loading");
  $("people").classList.add("loading");
  $("gallery").setAttribute("aria-busy", "true");
  viewChrome();
  filterChips();
  syncURL();
  try {
    if (state.view === "albums") {
      await loadAlbums();
      if (gen !== state.generation) return;
      renderAlbums();
      state.items = [];
      state.total = state.albums.length;
    } else if (state.view === "people") {
      const result = await api(
        "/api/people?" +
          new URLSearchParams({
            q: $("query").value.trim(),
            limit: 48,
            offset: state.offset,
          }),
      );
      if (gen !== state.generation) return;
      state.people = result.items;
      state.items = result.items;
      state.total = result.total;
      state.semantic = false;
      renderPeople();
      $("resultsCount").textContent = number(result.total) + " groups";
      if (!result.items.length)
        empty(
          "Faces will find their place.",
          "Face groups appear as photos are indexed. Search names or give an unnamed group a name.",
        );
    } else {
      const p = params();
      p.set("limit", "60");
      p.set("offset", state.offset);
      const result = await api("/api/search?" + p, undefined, signal);
      if (gen !== state.generation) return;
      state.bag = [];
      state.shuffleStarted = false;
      const annotations = await api("/api/curation", {
        ids: result.items.map((p) => p.id),
      });
      if (gen !== state.generation) return;
      state.items = result.items;
      state.total = result.total;
      state.semantic = result.semantic;
      state.annotations = annotations;
      renderPhotos();
      const q = $("query").value.trim();
      if (q) $("resultsTitle").textContent = "Results for “" + q + "”";
      $("resultsCount").textContent = result.semantic
        ? number(result.indexed_candidates) + " indexed candidates"
        : number(result.total) + " photos";
      if (result.semantic)
        notice(
          "Ranked by visual similarity. Content search covers indexed photos and needs the Mac worker for new queries.",
        );
      if (!result.items.length)
        empty(
          state.view === "favorites"
            ? "Your favorites start here."
            : state.view === "hidden"
              ? "Nothing tucked away."
              : state.view === "memories"
                ? "Another day, another memory."
                : "No photos in this selection.",
          state.view === "favorites"
            ? "Tap the heart on a photo to add it here."
            : "Try another search or filter. People and content results cover photos indexed so far.",
        );
    }
    if (gen === state.generation) pagination();
    return true;
  } catch (error) {
    if (gen === state.generation) {
      notice(error.message, true);
      state.items = [];
      state.total = 0;
      for (const id of ["gallery", "people", "albums"]) $(id).replaceChildren();
      pagination();
    }
    return false;
  } finally {
    if (gen === state.generation) {
      $("gallery").classList.remove("loading");
      $("people").classList.remove("loading");
      $("gallery").setAttribute("aria-busy", "false");
    }
  }
}
function renderPhotos() {
  const gallery = $("gallery");
  thumbnailObserver?.disconnect();
  gallery.replaceChildren();
  for (const [i, photo] of state.items.entries()) {
    const a = state.annotations[photo.id] || {};
    const card = text("article", "", "photo-card");
    card.classList.toggle("selected", state.selected.has(photo.id));
    const open = text("a", "", "photo-open");
    const photoQuery = params();
    photoQuery.set("image", photo.id);
    open.href = "/?" + photoQuery;
    open.setAttribute("aria-label", "View " + (a.title || photo.filename));
    const img = new Image();
    previewImage(img, "/api/images/" + photo.id + "/thumbnail");
    img.alt = a.title || photo.filename;
    img.loading = "lazy";
    img.decoding = "async";
    img.onerror = () => {
      img.classList.add("unavailable");
      img.alt = "Preview unavailable: " + photo.filename;
    };
    open.append(img);
    open.onclick = (e) => {
      if (!normalClick(e)) return;
      e?.preventDefault();
      return openImage(i);
    };
    const heart = text("button", a.favorite ? "♥" : "♡", "photo-heart");
    heart.setAttribute(
      "aria-label",
      (a.favorite ? "Unfavorite " : "Favorite ") + photo.filename,
    );
    heart.setAttribute("aria-pressed", String(!!a.favorite));
    heart.onclick = () => favorite(photo.id);
    const select = text("input");
    select.type = "checkbox";
    select.className = "photo-select";
    select.checked = state.selected.has(photo.id);
    select.setAttribute("aria-label", "Select " + photo.filename);
    select.onchange = () => {
      select.checked
        ? state.selected.add(photo.id)
        : state.selected.delete(photo.id);
      card.classList.toggle("selected", select.checked);
      selectionBar();
    };
    const info = text("div", "", "photo-info");
    info.append(
      text("span", a.title || photo.filename, "photo-name"),
      text("span", date(photo), "photo-date"),
    );
    card.append(open, heart, select, info);
    if (a.location) card.append(text("div", a.location, "photo-place"));
    if (a.hidden) card.append(text("span", "Hidden", "hidden-label"));
    gallery.append(card);
  }
}
function renderPeople() {
  const grid = $("people");
  thumbnailObserver?.disconnect();
  grid.replaceChildren();
  for (const p of state.people) {
    const card = text("article", "", "person-card");
    const open = text("a", "", "person-open");
    open.href = "/?person=" + p.id;
    const img = new Image();
    previewImage(img, p.image_url);
    img.alt = p.name || "Unnamed group " + p.id;
    img.loading = "lazy";
    open.append(
      img,
      text("h3", p.name || "Person " + p.id),
      text("p", number(p.photo_count) + " photos"),
    );
    open.onclick = (e) => {
      if (!normalClick(e)) return;
      e?.preventDefault();
      selectPerson(p);
    };
    const edit = text(
      "button",
      p.name ? "Edit name / merge" : "Add a name",
      "text-button",
    );
    edit.onclick = () => editPerson(p);
    card.append(open, edit);
    grid.append(card);
  }
}
async function loadAlbums() {
  const result = await api("/api/albums");
  state.albums = result.items;
  for (const id of ["bulkAlbum", "photoAlbum"]) {
    const select = $(id);
    const previous = select.value;
    select.replaceChildren(new Option("Choose an album…", ""));
    for (const a of state.albums) select.append(new Option(a.name, a.id));
    select.value = previous;
  }
}
function renderAlbums() {
  thumbnailObserver?.disconnect();
  $("albums").replaceChildren();
  $("resultsCount").textContent = number(state.albums.length) + " albums";
  for (const a of state.albums) {
    const card = text("article", "", "album-card");
    const open = text("button", "", "album-open");
    if (a.image_url) {
      const img = new Image();
      previewImage(img, a.image_url);
      img.alt = "";
      img.loading = "lazy";
      open.append(img);
    } else open.append(text("span", "▱", "album-placeholder"));
    open.append(
      text("h3", a.name),
      text("p", number(a.photo_count) + " photos"),
    );
    open.onclick = () => {
      resetInputs();
      state.album = a;
      state.view = "photos";
      state.offset = 0;
      load();
    };
    const actions = text("div", "", "album-actions");
    const rename = text("button", "Rename", "text-button");
    rename.onclick = () =>
      askName("Rename album", a.name, async (name) => {
        await api("/api/albums/" + a.id, { action: "rename", name });
        await load();
      });
    const del = text("button", "Remove album", "text-button");
    del.onclick = async () => {
      if (
        !confirm("Remove “" + a.name + "”? Photos will stay in your library.")
      )
        return;
      try {
        await api("/api/albums/" + a.id, { action: "delete" });
        await load();
        toast("Album removed. Photos kept.");
      } catch (e) {
        toast(e.message);
      }
    };
    actions.append(rename, del);
    card.append(open, actions);
    $("albums").append(card);
  }
  if (!state.albums.length)
    empty(
      "Make room for a new chapter.",
      "Create an album, then add photos from the library or photo details. Your folders stay as they are.",
    );
}
function selectionBar() {
  $("selectionBar").hidden = !state.selected.size;
  $("selectionCount").textContent = number(state.selected.size) + " selected";
}
async function favorite(id) {
  const old = state.annotations[id] || {};
  try {
    state.annotations[id] = await api("/api/curation/" + id, {
      favorite: !old.favorite,
    });
    renderPhotos();
    if (currentPhoto()?.id === id) updateFavorite();
    toast(old.favorite ? "Removed from favorites" : "Added to favorites");
  } catch (e) {
    toast(e.message);
  }
}
async function bulk(action) {
  if (state.busy) return;
  if (action === "album" && !$("bulkAlbum").value) {
    toast("Choose an album first");
    return;
  }
  state.busy = true;
  $("selectionBar").classList.add("loading");
  const ids = [...state.selected];
  let completed = 0;
  try {
    for (const id of ids) {
      if (action === "album")
        await api("/api/albums/" + $("bulkAlbum").value, {
          action: "add",
          image_id: id,
        });
      else
        await api(
          "/api/curation/" + id,
          action === "favorite"
            ? { favorite: true }
            : { hidden: state.view !== "hidden" },
        );
      completed++;
    }
    toast(number(completed) + " photos updated");
    await load();
    await loadAlbums();
  } catch (e) {
    toast(`${completed} of ${ids.length} updated. ${e.message}`);
  } finally {
    state.busy = false;
    $("selectionBar").classList.remove("loading");
  }
}
let nameAction;
function askName(title, value, action) {
  nameAction = action;
  $("nameDialogTitle").textContent = title;
  $("entryName").value = value;
  $("nameError").textContent = "";
  modal("nameDialog");
  $("entryName").focus();
}
$("nameForm").onsubmit = async (e) => {
  e.preventDefault();
  const button = e.submitter;
  button.disabled = true;
  try {
    await nameAction($("entryName").value.trim());
    closeModal("nameDialog");
  } catch (e) {
    $("nameError").textContent = e.message;
  } finally {
    button.disabled = false;
  }
};
$("cancelName").onclick = () => closeModal("nameDialog");
$("newAlbum").onclick = () =>
  askName("A new chapter.", "", async (name) => {
    await api("/api/albums", { name });
    await load();
    toast("Album created");
  });
async function loadSaved() {
  try {
    const result = await api("/api/collections");
    $("savedSelections").replaceChildren();
    if (!result.items.length)
      $("savedSelections").append(
        text("p", "Save a search for your frame.", "muted"),
      );
    for (const c of result.items) {
      const row = text("div", "", "saved-row");
      const b = text("button", c.name, "saved-link");
      b.onclick = () => {
        restoreQuery(c.query);
        state.offset = 0;
        state.savedName = c.name;
        load();
      };
      const del = text("button", "×", "saved-delete");
      del.setAttribute("aria-label", "Remove saved selection " + c.name);
      del.onclick = async () => {
        try {
          await api("/api/collections", { action: "delete", id: c.id });
          loadSaved();
        } catch (e) {
          toast(e.message);
        }
      };
      const link = text("button", "?", "saved-api");
      link.setAttribute("aria-label", "Playback links for " + c.name);
      link.onclick = () => showPlaybackLinks(c);
      row.append(b, link, del);
      $("savedSelections").append(row);
    }
  } catch (e) {
    toast(e.message);
  }
}
$("saveSelection").onclick = () => {
  if (!photoViews()) {
    toast("Open a photo selection or search first");
    return;
  }
  askName("Keep this selection.", "", async (name) => {
    await api("/api/collections", {
      name,
      query: Object.fromEntries(params()),
    });
    loadSaved();
    toast("Selection saved");
  });
};
function selectPerson(p) {
  resetInputs();
  state.person = p;
  state.view = "photos";
  state.offset = 0;
  load();
}
let mergeGeneration = 0,
  mergeTimer;
async function mergeOptions() {
  const gen = ++mergeGeneration;
  try {
    const result = await api(
      "/api/people?limit=200&q=" + encodeURIComponent($("mergeSearch").value),
    );
    if (gen !== mergeGeneration) return;
    $("mergeTarget").replaceChildren(new Option("Choose a person…", ""));
    for (const p of result.items)
      if (p.id !== state.editing.id)
        $("mergeTarget").append(
          new Option(
            `${p.name || "Person " + p.id} (${p.photo_count} photos)`,
            p.id,
          ),
        );
  } catch (e) {
    $("editError").textContent = e.message;
  }
}
async function editPerson(p) {
  state.editing = p;
  $("personName").value = p.name || "";
  $("editError").textContent = "";
  $("mergeSearch").value = "";
  modal("personEditor");
  $("personName").focus();
  mergeOptions();
}
$("mergeSearch").oninput = () => {
  clearTimeout(mergeTimer);
  mergeTimer = setTimeout(mergeOptions, 250);
};
$("personForm").onsubmit = async (e) => {
  e.preventDefault();
  try {
    const name = $("personName").value;
    await api("/api/people/" + state.editing.id, { name });
    if (state.person?.id === state.editing.id) state.person.name = name;
    closeModal("personEditor");
    await load();
    toast("Person name saved");
  } catch (e) {
    $("editError").textContent = e.message;
  }
};
$("mergePerson").onclick = async () => {
  const target = Number($("mergeTarget").value);
  if (!target) {
    $("editError").textContent = "Choose the group you want to keep.";
    return;
  }
  if (!confirm("Combine these face groups into the selected person?")) return;
  $("mergePerson").disabled = true;
  try {
    await api("/api/people/" + state.editing.id, { merge_into: target });
    if (state.person?.id === state.editing.id)
      state.person = {
        id: target,
        name: $("mergeTarget").selectedOptions[0].textContent.replace(
          / \(.*$/,
          "",
        ),
      };
    closeModal("personEditor");
    await load();
    refreshStatus();
    toast("Face groups merged");
  } catch (e) {
    $("editError").textContent = e.message;
  } finally {
    $("mergePerson").disabled = false;
  }
};
$("cancelPerson").onclick = () => closeModal("personEditor");
async function refreshStatus() {
  try {
    const s = await api("/api/indexing");
    state.status = s;
    $("libraryCount").textContent = number(s.total) + " memories";
    $("peopleBadge").textContent = number(s.people);
    $("workerDot").classList.toggle("online", s.worker_online && !s.paused);
    $("indexTitle").textContent = !s.enabled
      ? "AI indexing is off"
      : s.paused
        ? "Indexing paused"
        : s.last_error && !s.worker_online
          ? "Waiting for Mac"
          : s.pending
            ? "Finding your memories"
            : s.failed
              ? "Some photos need a retry"
              : "Library up to date";
    $("indexDetail").textContent = s.active
      ? "Working gently in the background"
      : s.paused
        ? "Your photos are still ready to view."
        : "Face groups and content search";
    $("progressBar").style.width =
      (s.total ? (100 * s.indexed) / s.total : 0) + "%";
    $("indexCounts").textContent =
      `${number(s.indexed)} / ${number(s.total)} indexed${s.failed ? " · " + number(s.failed) + " failed" : ""}`;
    $("pause").textContent = s.paused ? "Resume indexing" : "Pause indexing";
    $("pause").disabled = !s.enabled;
    $("retry").hidden = !s.failed;
    $("workerDetail").textContent =
      `${s.worker_url || "No worker configured"} · ${s.clip_model} · ${s.face_model}`;
    $("indexError").textContent = s.last_error || "";
  } catch (e) {
    $("indexTitle").textContent = "Service unavailable";
    $("indexDetail").textContent = "Reconnecting…";
  }
}
$("pause").onclick = async () => {
  try {
    await api("/api/indexing/control", {
      action: state.status?.paused ? "resume" : "pause",
    });
    refreshStatus();
  } catch (e) {
    toast(e.message);
  }
};
$("retry").onclick = async () => {
  try {
    await api("/api/indexing/control", { action: "retry" });
    refreshStatus();
  } catch (e) {
    toast(e.message);
  }
};
const currentPhoto = () => state.playbackPhoto || state.items[state.index];
function updateFavorite() {
  const a = state.annotations[currentPhoto()?.id] || {};
  $("viewerFavorite").textContent = a.favorite ? "♥ Favorited" : "♡ Favorite";
  $("viewerFavorite").setAttribute("aria-pressed", String(!!a.favorite));
}
function fillDetails(photo, a) {
  $("photoTitle").value = a.title || "";
  $("photoDescription").value = a.description || "";
  $("photoLocation").value = a.location || "";
  $("photoTags").value = (a.tags || []).join(", ");
  $("photoHidden").checked = !!a.hidden;
  $("fileDetails").textContent = JSON.stringify(
    {
      path: photo.path,
      taken_at: photo.taken_at,
      created_at: photo.created_at,
      width: photo.width,
      height: photo.height,
      format: photo.format,
      size_bytes: photo.size_bytes,
      exif: photo.exif,
    },
    null,
    2,
  );
  const gps = photo.exif?.GPS;
  $("gpsDetail").textContent =
    gps && Object.keys(gps).length
      ? "GPS metadata available in file details."
      : "No GPS metadata available.";
  $("detailStatus").textContent = "";
  state.dirty = false;
  renderPhotoAlbums(a);
}
function renderPhotoAlbums(a) {
  $("photoAlbums").replaceChildren();
  for (const id of a.albums || []) {
    const album = state.albums.find((x) => x.id === id);
    if (!album) continue;
    const b = text("button", album.name + " ×", "filter-chip");
    b.setAttribute("aria-label", "Remove photo from " + album.name);
    b.onclick = async () => {
      const photo = currentPhoto();
      try {
        await api("/api/albums/" + id, {
          action: "remove",
          image_id: photo.id,
        });
        const annotation = await api("/api/curation/" + photo.id);
        state.annotations[photo.id] = annotation;
        renderPhotoAlbums(annotation);
        toast("Removed from album");
      } catch (e) {
        toast(e.message);
      }
    };
    $("photoAlbums").append(b);
  }
}
async function openImage(i, external = null) {
  if (!state.items.length && !external) return;
  state.playbackPhoto = external;
  if (!external) state.index = (i + state.items.length) % state.items.length;
  const gen = ++state.viewerGeneration;
  const photo = currentPhoto();
  const a = state.annotations[photo.id] || {};
  $("fullImage").alt = a.title || photo.filename;
  $("fullImage").classList.add("loading");
  $("fullImage").onload = () => $("fullImage").classList.remove("loading");
  $("fullImage").onerror = () => {
    $("fullImage").classList.remove("loading");
    toast("This preview could not be loaded. Try the original.");
  };
  $("fullImage").src = photo.image_url + "?width=1920&height=1440";
  $("imageName").textContent = a.title || photo.filename;
  $("imageMeta").textContent = [
    date(photo),
    a.location || photo.directory,
    `${photo.width} × ${photo.height}`,
  ]
    .filter(Boolean)
    .join(" · ");
  $("original").href = photo.original_url;
  $("viewerPosition").textContent = external
    ? "Playing your selection"
    : `${number(state.offset + state.index + 1)} / ${number(state.total)} in selection`;
  $("imagePeople").replaceChildren();
  updateFavorite();
  fillDetails(photo, a);
  modal("viewer");
  const imageURL = new URL(location.href);
  imageURL.searchParams.set("image", photo.id);
  history.replaceState(null, "", imageURL.pathname + imageURL.search);
  void loadImageFaces(photo, gen);
}
async function loadImageFaces(photo, gen) {
  facesController?.abort();
  const controller = typeof AbortController === "function" ? new AbortController() : null;
  facesController = controller;
  try {
    const faces = await api(
      "/api/images/" +
        photo.id +
        "/faces" +
        (state.view === "hidden" ? "?hidden=only" : ""),
      undefined,
      controller?.signal,
    );
    if (gen !== state.viewerGeneration) return;
    const seen = new Set();
    for (const f of faces) {
      if (seen.has(f.person_id)) continue;
      seen.add(f.person_id);
      const b = text("button", f.name || "Name this face");
      b.onclick = () => {
        stopPlay();
        closeModal("viewer");
        editPerson({ id: f.person_id, name: f.name });
      };
      $("imagePeople").append(b);
    }
  } catch (e) {
    /* Photos remain usable while AI is unavailable. */
  }
}
function stopPlay() {
  clearTimeout(state.playing);
  state.playing = null;
  playbackController?.abort();
  clearTimeout(idleTimer);
  $("viewer").classList.remove("idle");
  if (wakeLock) {
    wakeLock.release().catch(() => {});
    wakeLock = null;
  }
  $("play").textContent = "▷ Play";
  $("viewer").classList.remove("playing");
}
function schedulePlay() {
  clearTimeout(state.playing);
  state.playing = setTimeout(
    async () => {
      await playbackStep();
      if (state.playing) schedulePlay();
    },
    Number($("slideSeconds").value) * 1000,
  );
}
function togglePlay() {
  if (state.playing) {
    stopPlay();
    return;
  }
  if (!$("detailsPanel").hidden) {
    $("detailsPanel").hidden = true;
    $("toggleDetails").setAttribute("aria-expanded", "false");
  }
  state.bag = [];
  state.shuffleStarted = false;
  $("viewer").classList.add("playing");
  $("play").textContent = "Ⅱ Pause";
  schedulePlay();
  showPlayerControls();
  acquireWakeLock();
}
function shuffledBag() {
  const bag = state.items.map((_, i) => i).filter((i) => i !== state.index);
  for (let j = bag.length - 1; j > 0; j--) {
    const k = Math.floor(Math.random() * (j + 1));
    [bag[j], bag[k]] = [bag[k], bag[j]];
  }
  return bag;
}
let advancing = false;
async function advance(direction, auto = false) {
  if (advancing || !state.items.length) return;
  if (state.dirty && !auto && !confirm("Discard unsaved photo details?"))
    return;
  advancing = true;
  try {
    if (state.shuffle && direction > 0) {
      if (!state.shuffleStarted) {
        state.bag = shuffledBag();
        state.shuffleStarted = true;
      }
      if (state.bag.length) {
        await openImage(state.bag.pop());
        return;
      }
    }
    const next = state.index + direction;
    if (next >= 0 && next < state.items.length && !state.shuffle) {
      await openImage(next);
      return;
    }
    const limit = 60;
    const cap = state.semantic ? Math.min(state.total, 1060) : state.total;
    state.offset =
      direction > 0
        ? state.offset + limit >= cap
          ? 0
          : state.offset + limit
        : state.offset === 0
          ? Math.floor((cap - 1) / limit) * limit
          : state.offset - limit;
    if (await load()) {
      state.index = direction > 0 ? 0 : state.items.length - 1;
      if (state.shuffle) {
        state.index = -1;
        state.bag = shuffledBag();
        await openImage(state.bag.pop() || 0);
      } else await openImage(state.index);
    } else stopPlay();
  } finally {
    advancing = false;
  }
}
$("viewer").addEventListener("close", () => {
  state.viewerGeneration++;
  state.playbackPhoto = null;
  state.playbackHistory = [];
  facesController?.abort();
  stopPlay();
  syncURL();
  if (document.fullscreenElement) document.exitFullscreen().catch(() => {});
});
$("closeViewer").onclick = () => {
  if (state.dirty && !confirm("Discard unsaved photo details?")) return;
  closeModal("viewer");
};
$("viewer").addEventListener("cancel", (e) => {
  if (state.dirty && !confirm("Discard unsaved photo details?"))
    e.preventDefault();
});
$("nextImage").onclick = () => {
  stopPlay();
  if (state.playbackPhoto) playbackStep();
  else advance(1);
};
$("prevImage").onclick = () => {
  stopPlay();
  if (state.playbackPhoto) playbackBack();
  else advance(-1);
};
$("play").onclick = togglePlay;
$("slideSeconds").onchange = () => {
  try {
    localStorage.setItem("homeImages.seconds", $("slideSeconds").value);
  } catch (e) {}
  if (state.playing) schedulePlay();
};
$("shuffle").onclick = () => {
  state.shuffle = !state.shuffle;
  state.bag = [];
  state.shuffleStarted = false;
  $("shuffle").textContent = state.shuffle ? "Shuffle on" : "Shuffle off";
  $("shuffle").setAttribute("aria-pressed", String(state.shuffle));
};
$("fullscreen").onclick = async () => {
  try {
    if (document.fullscreenElement) await document.exitFullscreen();
    else if ($("viewer").requestFullscreen)
      await $("viewer").requestFullscreen();
    else toast("Fullscreen is unavailable in this browser");
  } catch (e) {
    toast("Fullscreen is unavailable in this browser");
  }
};
$("slideshow").onclick = () => {
  if (state.view === "hidden") {
    toast("Restore photos before showing them in a slideshow");
    return;
  }
  if (state.items.length) {
    openImage(0);
    togglePlay();
  } else toast("Choose a selection with photos first");
};
$("viewerFavorite").onclick = () => favorite(currentPhoto().id);
$("toggleDetails").onclick = () => {
  stopPlay();
  $("detailsPanel").hidden = !$("detailsPanel").hidden;
  $("toggleDetails").setAttribute(
    "aria-expanded",
    String(!$("detailsPanel").hidden),
  );
};
$("detailsForm").oninput = () => (state.dirty = true);
$("detailsForm").onsubmit = async (e) => {
  e.preventDefault();
  const photo = currentPhoto();
  const gen = state.viewerGeneration;
  try {
    const a = await api("/api/curation/" + photo.id, {
      title: $("photoTitle").value,
      description: $("photoDescription").value,
      location: $("photoLocation").value,
      tags: $("photoTags").value.split(","),
      hidden: $("photoHidden").checked,
    });
    state.annotations[photo.id] = a;
    if (gen === state.viewerGeneration) {
      state.dirty = false;
      if (a.hidden && state.view !== "hidden") {
        closeModal("viewer");
        await load();
        toast("Hidden from your library and slideshow");
        return;
      }
      $("detailStatus").textContent = "Saved. Originals are unchanged.";
      $("imageName").textContent = a.title || photo.filename;
      renderPhotos();
    }
    toast("Photo details saved");
  } catch (e) {
    $("detailStatus").textContent = e.message;
  }
};
$("addToAlbum").onclick = async () => {
  const id = $("photoAlbum").value;
  if (!id) {
    toast("Choose an album first");
    return;
  }
  const photo = currentPhoto();
  try {
    await api("/api/albums/" + id, { action: "add", image_id: photo.id });
    const a = await api("/api/curation/" + photo.id);
    state.annotations[photo.id] = a;
    if (currentPhoto()?.id === photo.id) renderPhotoAlbums(a);
    toast("Added to album");
  } catch (e) {
    toast(e.message);
  }
};
$("favoriteSelected").onclick = () => bulk("favorite");
$("hideSelected").onclick = () => bulk("hidden");
$("addSelected").onclick = () => bulk("album");
$("clearSelection").onclick = () => {
  state.selected.clear();
  renderPhotos();
  selectionBar();
};
$("filtersToggle").onclick = () => {
  $("filters").hidden = !$("filters").hidden;
  $("filtersToggle").setAttribute(
    "aria-expanded",
    String(!$("filters").hidden),
  );
};
$("applyFilters").onclick = () => {
  state.offset = 0;
  state.savedName = "";
  load();
};
$("resetFilters").onclick = () => {
  for (const id of Object.values(fields)) $(id).value = "";
  state.album = null;
  state.offset = 0;
  state.savedName = "";
  load();
};
$("searchForm").onsubmit = (e) => {
  e.preventDefault();
  state.offset = 0;
  state.savedName = "";
  load();
};
for (const b of document.querySelectorAll("[data-query]"))
  b.onclick = () => {
    if (!photoViews()) setView("photos");
    $("query").value = b.dataset.query;
    $("mode").value = "semantic";
    state.offset = 0;
    load();
  };
for (const b of document.querySelectorAll("[data-view]"))
  b.onclick = () => setView(b.dataset.view);
$("clearPerson").onclick = () => {
  state.person = null;
  state.offset = 0;
  load();
};
$("refresh").onclick = () => {
  load();
  refreshStatus();
};
$("previous").onclick = () => {
  state.offset = Math.max(
    0,
    state.offset - (state.view === "people" ? 48 : 60),
  );
  load();
  window.scrollTo(0, 0);
};
$("next").onclick = () => {
  state.offset += state.view === "people" ? 48 : 60;
  load();
  window.scrollTo(0, 0);
};
$("browseFolders").onclick = async () => {
  const current = $("directory").value.trim();
  try {
    const result = await api(
      "/api/directories?limit=500&directory=" + encodeURIComponent(current),
    );
    $("folderList").replaceChildren();
    const options = [{ label: "All folders", value: "" }];
    if (current)
      options.push({
        label: "↑ Parent folder",
        value: current.split("/").slice(0, -1).join("/"),
      });
    for (const value of result.directories)
      options.push({ label: "▱ " + value.split("/").pop(), value });
    for (const option of options) {
      const b = text("button", option.label);
      b.onclick = () => {
        $("directory").value = option.value;
        $("folderList").hidden = true;
        state.offset = 0;
        load();
      };
      $("folderList").append(b);
    }
    $("folderList").hidden = false;
  } catch (e) {
    notice(e.message, true);
  }
};
for (const id of ["directory", "place", "tag", "dateFrom", "dateTo"])
  $(id).addEventListener("keydown", (e) => {
    if (e.key === "Enter") {
      $("applyFilters").click();
      e.preventDefault();
    }
  });
window.addEventListener("keydown", (e) => {
  if (!$("viewer").open || /INPUT|TEXTAREA|SELECT/.test(e.target.tagName))
    return;
  if (e.key === "ArrowRight") {
    e.preventDefault();
    stopPlay();
    if (state.playbackPhoto) playbackStep();
    else advance(1);
  }
  if (e.key === "ArrowLeft") {
    e.preventDefault();
    stopPlay();
    if (state.playbackPhoto) playbackBack();
    else advance(-1);
  }
  if (e.key === " ") {
    e.preventDefault();
    togglePlay();
  }
});
(async () => {
  const p = new URLSearchParams(location.search);
  restoreQuery(p);
  if (["people", "albums"].includes(p.get("view"))) state.view = p.get("view");
  state.offset = Math.max(0, Number(p.get("offset")) || 0);
  try {
    const s = localStorage.getItem("homeImages.seconds");
    if (["8", "15", "30", "60"].includes(s)) $("slideSeconds").value = s;
  } catch (e) {}
  await loadAlbums().catch((e) => toast(e.message));
  if (state.album)
    state.album =
      state.albums.find((a) => a.id === state.album.id) || state.album;
  if (p.get("order") === "random") {
    state.shuffle = true;
    $("shuffle").textContent = "Shuffle on";
    $("shuffle").setAttribute("aria-pressed", "true");
  }
  viewChrome();
  refreshStatus();
  loadSaved();
  await load();
  if (p.get("image")) {
    try {
      const photo = await api("/api/images/" + encodeURIComponent(p.get("image")));
      state.annotations[photo.id] = await api("/api/curation/" + photo.id);
      await openImage(0, photo);
    } catch (e) { notice(e.message, true); }
  }
  if (
    p.get("frame") === "true" &&
    state.items.length &&
    state.view !== "hidden"
  ) {
    await openImage(0);
    togglePlay();
  }
  setInterval(refreshStatus, 10000);
})();
let playbackLinkQuery = null;
const playbackLinkButton = text("button", "Frame & API links", "secondary");
playbackLinkButton.id = "playbackLinks";
$("refresh").before(playbackLinkButton);
playbackLinkButton.onclick = () => showPlaybackLinks();
function showPlaybackLinks(saved = null) {
  if (!photoViews() && !saved) return;
  playbackLinkQuery = saved
    ? {
        api: new URLSearchParams({ collection: saved.id }),
        frame: new URLSearchParams(saved.query),
      }
    : { api: params(), frame: params() };
  $("playbackOrder").value = state.shuffle ? "random" : "sequential";
  updatePlaybackLinks();
  modal("playbackDialog");
}
function updatePlaybackLinks() {
  const order = $("playbackOrder").value;
  const apiQuery = new URLSearchParams(playbackLinkQuery.api);
  apiQuery.set("order", order);
  $("apiURL").value = location.origin + "/api/playback/next?" + apiQuery;
  const frameQuery = new URLSearchParams(playbackLinkQuery.frame);
  frameQuery.set("frame", "true");
  frameQuery.set("order", order);
  $("frameURL").value = location.origin + "/?" + frameQuery;
}
$("playbackOrder").onchange = updatePlaybackLinks;
$("closePlayback").onclick = () => closeModal("playbackDialog");
async function copyField(id) {
  try {
    await navigator.clipboard.writeText($(id).value);
    toast("Link copied");
  } catch (e) {
    $(id).focus();
    $(id).select();
    toast("Select and copy this link");
  }
}
$("copyApi").onclick = () => copyField("apiURL");
$("copyFrame").onclick = () => copyField("frameURL");
async function playbackStep() {
  const viewerGeneration = state.viewerGeneration;
  if (state.dirty && !confirm("Discard unsaved photo details?")) return;
  playbackController?.abort();
  const controller = typeof AbortController === "function" ? new AbortController() : null;
  playbackController = controller;
  $("viewerPosition").textContent = "Loading next photo…";
  try {
    const previous = currentPhoto();
    const p = params();
    p.set("order", state.shuffle ? "random" : "sequential");
    if (previous) p.set("after", previous.id);
    const next = await api("/api/playback/next?" + p, undefined, controller?.signal);
    if (!$("viewer").open || viewerGeneration !== state.viewerGeneration)
      return;
    if (previous) {
      state.playbackHistory.push(previous);
      if (state.playbackHistory.length > 100) state.playbackHistory.shift();
    }
    state.annotations[next.id] = await api("/api/curation/" + next.id, undefined, controller?.signal);
    if (!$("viewer").open || viewerGeneration !== state.viewerGeneration)
      return;
    await openImage(0, next);
  } catch (e) {
    if (e.name !== "AbortError") toast("Playback: " + e.message);
  } finally {
    if (playbackController === controller) playbackController = null;
  }
}
async function playbackBack() {
  if (state.dirty && !confirm("Discard unsaved photo details?")) return;
  const previous = state.playbackHistory.pop();
  if (!previous) return;
  await openImage(0, previous);
}
const toggleSaved = text("button", "Saved", "nav");
toggleSaved.id = "toggleSaved";
toggleSaved.setAttribute("aria-expanded", "false");
toggleSaved.onclick = () => {
  const open = document
    .querySelector(".sidebar")
    .classList.toggle("show-saved");
  toggleSaved.setAttribute("aria-expanded", String(open));
};
document.querySelector("nav").append(toggleSaved);
let idleTimer, wakeLock;
function showPlayerControls() {
  const viewer = $("viewer");
  viewer.classList.remove("idle");
  clearTimeout(idleTimer);
  if (state.playing)
    idleTimer = setTimeout(() => viewer.classList.add("idle"), 3500);
}
$("viewer").addEventListener("pointermove", showPlayerControls);
$("viewer").addEventListener("pointerdown", showPlayerControls);
$("viewer").addEventListener("keydown", showPlayerControls);
async function acquireWakeLock() {
  try {
    if (navigator.wakeLock && !wakeLock)
      wakeLock = await navigator.wakeLock.request("screen");
  } catch (e) {
    /* Screen wake lock is optional on LAN HTTP and older browsers. */
  }
}
document.addEventListener("visibilitychange", () => {
  if (document.visibilityState === "visible" && state.playing)
    acquireWakeLock();
});
