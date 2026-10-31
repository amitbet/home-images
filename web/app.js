"use strict";
const $ = (id) => document.getElementById(id);
const state = {
  view: "photos",
  layout: "grid",
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
  grouped: false,
  groupScope: null,
  groupScopes: [],
  peopleFilters: [],
  navigation: [],
  dirty: false,
  loadingGrid: false,
  loadingMore: false,
  moreFailed: false,
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
// Keep recent thumbnails in this tab as well as the browser's HTTP cache.
// Bound both memory and work; old pages must not occupy the decoder queue.
const clientThumbnails = new Map();
const clientThumbnailBudget = 32 * 1024 * 1024;
let clientThumbnailBytes = 0, thumbnailQueue = [], thumbnailActive = 0;
const thumbnailRequests = new Set();
function resetThumbnailRequests() {
  thumbnailObserver?.disconnect();
  thumbnailQueue = [];
  for (const controller of thumbnailRequests) controller.abort();
}
function cacheThumbnail(url, blob) {
  const old = clientThumbnails.get(url);
  // Foreground and prefetch can finish together. Keep the first object URL alive
  // while an Image is decoding it instead of revoking it in the second response.
  if (old && old.expires > Date.now()) {
    clientThumbnails.delete(url);
    clientThumbnails.set(url, old);
    return old;
  }
  if (old) { URL.revokeObjectURL(old.src); clientThumbnailBytes -= old.size; }
  const entry = { src: URL.createObjectURL(blob), size: blob.size, expires: Date.now() + 300000 };
  clientThumbnails.delete(url);
  clientThumbnails.set(url, entry);
  clientThumbnailBytes += entry.size;
  while (clientThumbnailBytes > clientThumbnailBudget || clientThumbnails.size > 512) {
    const key = clientThumbnails.keys().next().value;
    const removed = clientThumbnails.get(key);
    clientThumbnails.delete(key);
    clientThumbnailBytes -= removed.size;
    URL.revokeObjectURL(removed.src);
  }
  return entry;
}
function requestThumbnail(img) {
  thumbnailQueue.push({ img, url: img.dataset.preview, generation: state.generation });
  pumpThumbnails();
}
function pumpThumbnails() {
  if ($("viewer").open) return;
  while (thumbnailActive < 4 && thumbnailQueue.length) {
    const job = thumbnailQueue.shift();
    if (!job.img.isConnected || job.generation !== state.generation) continue;
    const cached = clientThumbnails.get(job.url);
    if (cached && cached.expires > Date.now()) {
      clientThumbnails.delete(job.url);
      clientThumbnails.set(job.url, cached);
      job.img.src = cached.src;
      continue;
    }
    const controller = new AbortController();
    thumbnailRequests.add(controller);
    thumbnailActive++;
    const timeout = setTimeout(() => controller.abort(), 30000);
    fetch(job.url, { signal: controller.signal, cache: "default" })
      .then(response => {
        if (!response.ok) throw Error("Thumbnail " + response.status);
        return response.blob();
      })
      .then(blob => {
        if (controller.signal.aborted || job.generation !== state.generation || !job.img.isConnected) return;
        job.img.src = cacheThumbnail(job.url, blob).src;
      })
      .catch(error => {
        if (error.name !== "AbortError" && job.generation === state.generation && job.img.isConnected) {
          job.img.classList.add("unavailable");
          job.img.alt = "Preview unavailable";
        }
      })
      .finally(() => {
        clearTimeout(timeout);
        thumbnailRequests.delete(controller);
        thumbnailActive--;
        pumpThumbnails();
      });
  }
}
const thumbnailObserver = typeof IntersectionObserver === "function"
  ? new IntersectionObserver((entries) => {
      for (const entry of entries) if (entry.isIntersecting) {
        requestThumbnail(entry.target);
        thumbnailObserver.unobserve(entry.target);
      }
    }, { rootMargin: "150px" })
  : null;
function previewImage(img, url) {
  img.dataset.preview = url;
  if (thumbnailObserver) thumbnailObserver.observe(img);
  else queueMicrotask(() => requestThumbnail(img));
}
const fields = {
  directory: "directory",
  media_type: "mediaType",
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
  if (body !== undefined && path !== "/api/curation" && !path.startsWith("/api/faces?" ) && path !== "/api/faces") gridPages.clear();
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
  if ($("photoGroup").value) { p.set("group_by", $("photoGroup").value); p.set("group_view", "folders"); }
  if (state.groupScopes.length) p.set("group_scopes", JSON.stringify(state.groupScopes));
  if (state.peopleFilters.length) p.set("people_filter", JSON.stringify(state.peopleFilters.map(p => String(p.id))));
  p.set("sort", $("photoSort").value);
  p.set("direction", $("sortDirection").value);
  p.set("layout", state.layout);
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
  state.groupScope = null;
  state.groupScopes = [];
  state.peopleFilters = [];
}
function restoreQuery(query) {
  resetInputs();
  const p =
    query instanceof URLSearchParams ? query : new URLSearchParams(query);
  $("photoSort").value = "taken_at";
  try { state.groupScopes = JSON.parse(p.get("group_scopes") || "[]"); state.peopleFilters = JSON.parse(p.get("people_filter") || "[]").map(id => ({id:Number(id), name:"Person " + id})); } catch { state.groupScopes = []; state.peopleFilters = []; }
  if (!state.groupScopes.length && p.has("group_kind")) state.groupScopes = [{kind:p.get("group_kind"),key:p.get("group_key") || "",label:p.get("group_label") || p.get("group_key") || "Root folder"}];
  state.groupScope = state.groupScopes.at(-1) || null;
  if ([...$("photoSort").options].some(o => o.value === p.get("sort"))) $("photoSort").value = p.get("sort");
  $("sortDirection").value = p.get("direction") === "asc" ? "asc" : "desc";
  $("photoGroup").value = [...$("photoGroup").options].some(o => o.value === p.get("group_by")) ? p.get("group_by") : "";
  state.layout = p.get("layout") === "list" ? "list" : "grid";
  $("query").value = p.get("q") || "";
  $("mode").value = p.get("mode") === "metadata" ? "metadata" : "semantic";
  for (const [key, id] of Object.entries(fields))
    $(id).value = p.get(key) || "";
  if (p.get("person")) state.person = { id: Number(p.get("person")), name: "" };
  if (p.get("album"))
    state.album = { id: Number(p.get("album")), name: "Album" };
  state.view =
    ["people","albums"].includes(p.get("view")) ? p.get("view") :
    p.get("favorite") === "true"
      ? "favorites"
      : p.get("hidden") === "only"
        ? "hidden"
        : p.get("on_this_day") === "true"
          ? "memories"
          : "photos";
}
const viewTitles = {
  photos: "All photos",
  favorites: "Favorites",
  people: "People",
  albums: "Albums",
  memories: "On this day",
  hidden: "Hidden photos",
};
function viewChrome() {
  document.querySelectorAll("[data-view]").forEach((e) => {
    e.classList.toggle("active", e.dataset.view === state.view);
    if (e.dataset.view === state.view) e.setAttribute("aria-current", "page");
    else e.removeAttribute("aria-current");
  });
  $("resultsTitle").textContent =
    state.groupScope?.label || state.savedName || state.album?.name || viewTitles[state.view] || viewTitles.photos;
  $("query").placeholder =
    state.view === "people"
      ? "Search people"
      : "Search photos";
  $("searchForm").hidden = state.view === "albums";
  $("mode").hidden = state.view === "people";
  $("photoDisplayControls").hidden = !photoViews();
  const ranked = $("mode").value === "semantic" && !!$("query").value.trim();
  for (const id of ["photoSort", "sortDirection"]) {
    $(id).disabled = ranked;
    $(id).title = ranked ? "Content searches are ranked by visual similarity" : "";
  }
  $("photoGroup").disabled = false;
  if (ranked && $("photoGroup").value) {
    $("sortDirection").disabled = false;
    $("sortDirection").title = "Group order; photos stay ranked by visual similarity";
  }
  $("gallery").classList.toggle("list-view", state.layout === "list" && !$("photoGroup").value);
  $("listToggle").textContent = state.layout === "list" ? "Grid view" : "List view";
  $("listToggle").hidden = !!$("photoGroup").value;
  $("toggleGridFaces").hidden = !!$("photoGroup").value;
  $("listToggle").setAttribute("aria-pressed", String(state.layout === "list"));
  $("slideshow").hidden = !photoViews() || !!$("photoGroup").value;
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
  renderBreadcrumbs();
  personChip();
  $("hideSelected").textContent =
    state.view === "hidden" ? "Restore to frame" : "Hide from frame";
}
function renderBreadcrumbs() {
  const query=params();query.set("view",state.view);
  for(const key of ["offset","layout","sort","direction"])query.delete(key);
  const key=query.toString(),label=($("photoGroup").value ? ($("photoGroup").value === "directory" ? "Directories" : $("photoGroup").selectedOptions[0].textContent) : state.groupScope?.label || state.person?.name || state.album?.name || ($("query").value.trim() ? "Search: "+$("query").value.trim() : viewTitles[state.view])) || "Photos";
  if (!state.navigation.length && state.groupScope) {
    const parent = new URLSearchParams(query);
    parent.delete("group_scopes");
    const ancestors = state.groupScopes.slice(0, -1);
    if (ancestors.length) parent.set("group_scopes", JSON.stringify(ancestors));
    for (const field of ["group_kind", "group_key", "group_label"]) parent.delete(field);
    parent.set("group_by", state.groupScope.kind);
    parent.set("group_view", "folders");
    const option = [...$("photoGroup").options].find(o => o.value === state.groupScope.kind);
    state.navigation.push({key: parent.toString(), label: option?.textContent || "Groups"});
  }
  if(!state.navigation.length || state.navigation.at(-1).key!==key)state.navigation.push({key,label});
  const nav=$("breadcrumbs");nav.replaceChildren();
  const home=text("button","All photos");home.onclick=()=>{state.navigation=[];restoreQuery("");state.offset=0;load();window.scrollTo(0,0);};nav.append(home);
  state.navigation.forEach((entry,index)=>{
    if(index===0 && entry.label==="All photos" && !new URLSearchParams(entry.key).get("group_kind"))return;
    nav.append(text("span"," / "));const b=text("button",entry.label);
    b.onclick=()=>{state.navigation=state.navigation.slice(0,index+1);restoreQuery(entry.key);state.offset=0;load();window.scrollTo(0,0);};nav.append(b);
  });
}
async function enterPhotoGroup(kind,key,label,photoID) {
  state.groupScope={kind,key,label};state.groupScopes.push(state.groupScope);$("photoGroup").value="";state.offset=0;state.layout="grid";
  const opening = load();
  window.scrollTo(0, 0);
  if(await opening) {if(photoID){const i=state.items.findIndex(p=>p.id===photoID);if(i>=0)await openImage(i);else await openImage(0,await api("/api/images/"+photoID));}}
}
function renderPhotoGroupFolders(result, append = false) {
  const gallery=$("gallery");if (!append) gallery.replaceChildren();
  for(const group of result.items){
    const card=text("button","","directory-card"),img=new Image();img.alt="";previewImage(img,"/api/images/"+group.cover+"/thumbnail");
    card.append(img,text("strong",group.label || group.key || "Root folder"),text("span",number(group.count)+" photos"));
    card.onclick=()=>enterPhotoGroup(result.group_by || "directory",group.key,group.label || group.key || "Root folder");gallery.append(card);
  }
}
function personChip() {
  $("clearPerson").hidden = !state.person || !photoViews();
  if (state.person)
    $("clearPerson").textContent =
      (state.person.name || "Person " + state.person.id) + " ×";
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
function gridCap() {
  return state.semantic && !state.grouped ? Math.min(state.total, 1000) : state.total;
}
function hasMoreGrid() {
  return state.view !== "albums" && state.items.length > 0 && state.offset + state.items.length < gridCap();
}
function pagination() {
  $("previous").hidden = state.offset === 0 || state.view === "albums";
  $("next").hidden = !hasMoreGrid();
  $("next").disabled = state.loadingMore;
  $("next").textContent = state.loadingMore ? "Loading..." : state.moreFailed ? "Retry loading more" : "Load more";
  $("pageLabel").textContent = state.view !== "albums" && state.total
    ? `${number(state.offset + 1)}-${number(state.offset + state.items.length)} of ${number(gridCap())}` : "";
}
async function loadMoreGrid() {
  if (state.loadingGrid || state.loadingMore || !hasMoreGrid()) return;
  const gen = state.generation;
  const signal = searchController?.signal;
  state.loadingMore = true;
  state.moreFailed = false;
  pagination();
  try {
    const offset = state.offset + state.items.length;
    const p = params();
    p.set("offset", offset);
    p.set("limit", state.view === "people" ? 48 : Math.min(60, gridCap() - offset));
    let result;
    if (state.view === "people") {
      result = await api("/api/people?" + new URLSearchParams({q: $("query").value.trim(), limit: 48, offset, summary: true}), undefined, signal);
    } else result = await gridPage(p, signal);
    if (gen !== state.generation) return;
    const start = state.items.length;
    let annotations;
    if (state.view !== "people" && !result.directory_groups && !result.photo_groups) {
      annotations = await api("/api/curation", {ids: result.items.map(p => p.id)}, signal);
      if (gen !== state.generation) return;
    }
    state.total = result.total;
    state.items.push(...result.items);
    if (state.view === "people") {
      state.people = state.items;
      renderPeople(start);
    } else if (result.directory_groups || result.photo_groups) {
      renderPhotoGroupFolders(result, true);
    } else {
      Object.assign(state.annotations, annotations);
      renderPhotos(start);
      void loadThumbnailFaces(gen, result.items);
    }
    if (!result.items.length) state.total = offset;
  } catch (error) {
    if (gen === state.generation && error.name !== "AbortError") {
      state.moreFailed = true;
      notice("Could not load more. " + error.message, true);
    }
  } finally {
    if (gen === state.generation) {
      state.loadingMore = false;
      pagination();
    }
  }
}
const gridPages = new Map();
async function gridPage(p, signal) {
  const key = p.toString();
  const cached = gridPages.get(key);
  if (cached && Date.now() - cached.created < 30000) return cached.result;
  const result = await api("/api/search?" + p, undefined, signal);
  gridPages.delete(key);
  gridPages.set(key, {result, created: Date.now()});
  while (gridPages.size > 12) gridPages.delete(gridPages.keys().next().value);
  return result;
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
          (key === "media_type"
            ? "Type"
            : key === "directory"
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
  for (const person of state.peopleFilters) chips.push({label:"Person: " + person.name, clear:()=>{state.peopleFilters = state.peopleFilters.filter(p=>p.id !== person.id);}});
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
async function load(forcePeopleRefresh = false) {
  if (searchController) searchController.abort();
  searchController = typeof AbortController === "function" ? new AbortController() : null;
  const signal = searchController?.signal;
  const gen = ++state.generation;
  state.loadingGrid = true;
  state.loadingMore = false;
  state.moreFailed = false;
  state.faceMap = {};
  resetThumbnailRequests();
  for (const id of ["gallery", "people", "albums"]) $(id).replaceChildren();
  state.items = [];
  $("resultsCount").textContent = "Loading...";
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
            summary: true,
            refresh: forcePeopleRefresh === true ? 1 : 0,
          }),
        undefined,
        signal,
      );
      if (gen !== state.generation) return;
      state.people = result.items;
      state.items = [...result.items];
      state.total = result.total;
      state.semantic = false;
      state.grouped = false;
      renderPeople();
      $("resultsCount").textContent = number(result.total) + " groups";
      if (!result.items.length)
        empty(
          "No people yet",
          "Face groups appear as photos are indexed.",
        );
    } else {
      const p = params();
      p.set("limit", "60");
      p.set("offset", state.offset);
      const result = await gridPage(p, signal);
      if (gen !== state.generation) return;
      state.bag = [];
      state.shuffleStarted = false;
      if(result.directory_groups || result.photo_groups){state.items=[...result.items];state.total=result.total;state.semantic=false;state.grouped=false;renderPhotoGroupFolders(result);$("resultsCount").textContent=number(result.total)+(result.group_by === "directory" || result.directory_groups ? " directories" : " groups");if(!result.items.length)empty("No groups found", "Try changing your filters.");pagination();return true;}
      const annotations = await api("/api/curation", {
        ids: result.items.map((p) => p.id),
      });
      if (gen !== state.generation) return;
      state.items = [...result.items];
      state.total = result.total;
      state.semantic = result.semantic;
      state.grouped = !!result.group_by;
      state.annotations = annotations;
      renderPhotos();
      void loadThumbnailFaces(gen);
      const q = $("query").value.trim();
      if (q) $("resultsTitle").textContent = "Results for “" + q + "”";
      $("resultsCount").textContent = result.group_by ? `${number(result.unique_total)} photos; ${number(result.total)} group entries` : result.semantic
        ? number(result.indexed_candidates) + " indexed candidates"
        : number(result.total) + " photos";
      if (result.semantic)
        notice(
          result.group_by ? `Top ${number(result.unique_total)} matching photos grouped; ranked by visual similarity within each group.` : "Ranked by visual similarity across indexed photos.",
        );
      if (!result.items.length)
        empty(
          state.view === "favorites"
            ? "No favorites yet"
            : state.view === "hidden"
              ? "No hidden photos"
              : state.view === "memories"
                ? "No photos from this day"
                : "No photos found",
          state.view === "favorites"
            ? "Tap the heart on a photo to add it here."
            : "",
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
      state.loadingGrid = false;
      $("gallery").classList.remove("loading");
      $("people").classList.remove("loading");
      $("gallery").setAttribute("aria-busy", "false");
    }
  }
}
function renderPhotos(start = 0) {
  const gallery = $("gallery");
  if (!start) { thumbnailObserver?.disconnect(); gallery.replaceChildren(); }
  let lastGroup = start ? state.items[start - 1]?.group_key : null;
  for (const [i, photo] of state.items.entries()) {
    if (i < start) continue;
    if (photo.group_key !== undefined && photo.group_key !== lastGroup) {
      const heading=text("button",photo.group_label,"photo-group-heading");heading.onclick=()=>enterPhotoGroup($("photoGroup").value,photo.group_key,photo.group_label);gallery.append(heading);
      lastGroup = photo.group_key;
    }
    const a = state.annotations[photo.id] || {};
    const card = text("article", "", "photo-card");
    card.dataset.id = photo.id;
    card.classList.toggle("selected", state.selected.has(photo.id));
    const open = text("a", "", "photo-open");
    const photoQuery = params();
    photoQuery.set("image", photo.id);
    open.href = "/?" + photoQuery;
    open.setAttribute("aria-label", "View " + (a.title || displayFilename(photo.filename)));
    const img = new Image();
    previewImage(img, "/api/images/" + photo.id + "/thumbnail");
    if (photo.format === "VIDEO") open.append(text("span", "▶ Video", "video-badge"));
    img.alt = a.title || displayFilename(photo.filename);
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
      if(state.grouped)return enterPhotoGroup($("photoGroup").value,photo.group_key,photo.group_label,photo.id);
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
      for (const sibling of gallery.querySelectorAll(".photo-card")) if (sibling.dataset.id === photo.id) {
        sibling.classList.toggle("selected", select.checked);
        sibling.querySelector(".photo-select").checked = select.checked;
      }
      selectionBar();
    };
    const info = text("div", "", "photo-info");
    info.append(
      text("span", a.title || displayFilename(photo.filename), "photo-name"),
      text("span", date(photo), "photo-date"),
      text("span", [a.location || a.auto_location, photo.directory].filter(Boolean).join(" · "), "photo-details"),
    );
    card.append(open, heart, select, info);
    thumbnailFaces(card, img);
    if (a.location || a.auto_location) card.append(text("div", a.location || a.auto_location, "photo-place"));
    if (a.hidden) card.append(text("span", "Hidden", "hidden-label"));
    gallery.append(card);
  }
}
function renderPeople(start = 0) {
  const grid = $("people");
  if (!start) { thumbnailObserver?.disconnect(); grid.replaceChildren(); }
  for (const p of state.people.slice(start)) {
    const card = text("article", "", "person-card");
    card.dataset.person = p.id;
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
      "Name / review matches",
      "text-button",
    );
    edit.onclick = () => editPerson(p);
    card.append(open, edit);
    grid.append(card);
  }
}
// Update one People card in place so renaming keeps the grid and its loaded portraits.
function updatePersonCard(p) {
  const card = document.querySelector(`.person-card[data-person="${p.id}"]`);
  if (!card) return;
  card.querySelector("h3").textContent = p.name || "Person " + p.id;
  card.querySelector("p").textContent = number(p.photo_count) + " photos";
  card.querySelector("img").alt = p.name || "Unnamed group " + p.id;
  card.querySelector(".text-button").textContent = p.name
    ? "Edit name / merge"
    : "Add a name";
}
// Photos holding both merged groups count once, so take counts from the server
// and update the cards that are already shown.
async function refreshPersonCounts() {
  if (state.view !== "people") return;
  try {
    const result = await api(
      "/api/people?" +
        new URLSearchParams({ q: $("query").value.trim(), limit: 48, offset: state.offset }),
    );
    for (const fresh of result.items) {
      const p = state.people.find((p) => p.id === fresh.id);
      if (p && p.photo_count !== fresh.photo_count) {
        p.photo_count = fresh.photo_count;
        updatePersonCard(p);
      }
    }
  } catch (e) {
    /* Counts refresh on the next navigation. */
  }
}
function removePersonCard(id) {
  document.querySelector(`.person-card[data-person="${id}"]`)?.remove();
  state.people = state.people.filter((p) => p.id !== id);
  state.total = Math.max(0, state.total - 1);
  if (state.view === "people")
    $("resultsCount").textContent = number(state.total) + " groups";
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
      "No albums yet",
      "Create an album, then add photos from the library or photo details.",
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
      $("savedSelections").append(text("p", "None yet", "muted"));
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
const mergeSelection = new Map();
let mergeSelectedImages=null, mergeCountKey="", mergeCountTimer, mergeCountController;
function renderMergeImageCount(){
 const count=mergeSelectedImages===null?"counting images...":`${number(mergeSelectedImages)} images`;
 $("mergeSelectionCount").textContent=mergeSelection.size?`${mergeSelection.size} groups selected - ${count}`:"No groups selected";
 if($("mergeConfirm").open){
  $("mergeKeepName").textContent=`Keep ${state.editing.name||"Person "+state.editing.id} and add these ${mergeSelection.size} groups (${count}):`;
  $("confirmMergeGroups").disabled=mergeSelectedImages===null;
 }
}
function updateMergeImageCount(){
 const key=mergeSelection.size?`${state.editing.id}|${[...mergeSelection.keys()].sort((a,b)=>a-b).join(",")}`:"";
 if(key===mergeCountKey){renderMergeImageCount();return;}
 mergeCountKey=key;mergeSelectedImages=null;clearTimeout(mergeCountTimer);mergeCountController?.abort();renderMergeImageCount();
 if(!key)return;
 mergeCountTimer=setTimeout(async()=>{
  const controller=new AbortController();mergeCountController=controller;
  try{
   const result=await api(`/api/people/${state.editing.id}/merge-preview`,{sources:[...mergeSelection.keys()]},controller.signal);
   if(key!==mergeCountKey)return;
   mergeSelectedImages=result.images;renderMergeImageCount();
  }catch(e){if(e.name!=="AbortError"&&key===mergeCountKey){$("editError").textContent=`Could not count selected images: ${e.message}. Change the selection to retry.`;}}
 },150);
}
let suggestedGroups = [];
let suggestionPool = [];
let suggestionShown = 0;
let mergeGridDirty = false;
let suggestionGeneration = 0;
$("selectAllSuggestions").onclick = () => {
  const allSelected = suggestedGroups.every(p => mergeSelection.has(p.id));
  for (const p of suggestedGroups) {
    if (allSelected) mergeSelection.delete(p.id);
    else mergeSelection.set(p.id, p);
  }
  updateMergeSelection();
};
function updateMergeSelection() {
  $("selectAllSuggestions").disabled = !suggestedGroups.length;
  $("selectAllSuggestions").textContent = suggestedGroups.length && suggestedGroups.every(p => mergeSelection.has(p.id)) ? "Clear displayed selections" : "Select all displayed";
  updateMergeImageCount();
  $("mergePerson").disabled = !mergeSelection.size || mergeSelection.size > 5000;
  $("refreshSuggestions").disabled=!!mergeSelection.size;
  for (const input of document.querySelectorAll("input[data-merge-group]")) {
    input.checked = mergeSelection.has(Number(input.dataset.mergeGroup));
    input.closest(".face-suggestion").classList.toggle("selected", input.checked);
  }
}
function mergeChoice(person) {
  const label = document.createElement("label");
  label.className = "merge-choice";
  const input = document.createElement("input");
  input.type = "checkbox";
  input.dataset.mergeGroup = person.id;
  input.checked = mergeSelection.has(person.id);
  input.onchange = () => {
    if (input.checked) mergeSelection.set(person.id, person);
    else mergeSelection.delete(person.id);
    updateMergeSelection();
  };
  input.setAttribute("aria-label", `Select ${person.name || "Person " + person.id} for merge`);
  label.title = input.getAttribute("aria-label");
  label.append(input);
  return label;
}
function mergeGroupRow(person) {
  const row = text("article", "", "face-suggestion merge-card");
  const image = new Image();
  previewImage(image, person.image_url || `/api/people/${person.id}/image`);
  image.alt = person.name || `Person ${person.id}`;
  image.width = image.height = 88; image.loading = "lazy";
  const info = text("div", "", "merge-card-info");
  const years = person.first_year
    ? person.first_year === person.last_year ? `${person.first_year}` : `${person.first_year}\u2013${person.last_year}`
    : "Year unknown";
  const year = text("span", years, "merge-years");
  year.title = "Years taken (from photo capture dates)";
  info.append(text("strong", image.alt), text("p", `${person.photo_count} photos`), year);
  row.append(image, info, mergeChoice(person));
  return row;
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
    state.mergeCandidates = result.items;
    $("mergeTarget").replaceChildren(new Option("Choose a person...", ""));
    $("mergeResults").replaceChildren();
    for (const p of result.items) if (p.id !== state.editing.id) {
      $("mergeTarget").append(new Option(`${p.name || "Person " + p.id} (${p.photo_count} photos)`, p.id));
      $("mergeResults").append(mergeGroupRow(p));
    }
    updateMergeSelection();
  } catch (e) {
    $("editError").textContent = e.message;
  }
}
// Both naming dialogs share the lightweight autocomplete and cancellation rules.
function nameAutocomplete(inputId, listId, dialogId, onMatches) {
  const input = $(inputId), list = $(listId);
  let timer, controller, generation = 0;
  const cancel = () => {
    clearTimeout(timer);
    controller?.abort();
    generation++;
  };
  const load = async () => {
    cancel();
    const current = generation, name = input.value.trim();
    controller = new AbortController();
    try {
      const result = await api("/api/people/names?limit=50&q=" + encodeURIComponent(name), undefined, controller.signal);
      if (current !== generation || input.value.trim() !== name) return;
      list.replaceChildren(...result.items.map(p => new Option(p.name)));
      onMatches(result.items);
    } catch (e) {
      if (e.name !== "AbortError" && current === generation) onMatches([]);
    }
  };
  const reset = () => {
    cancel();
    list.replaceChildren();
    onMatches([]);
  };
  input.addEventListener("input", () => {
    reset();
    timer = setTimeout(load, 200);
  });
  $(dialogId).addEventListener("close", cancel);
  return {load, reset, cancel};
}
const personNameAutocomplete = nameAutocomplete("personName", "personNameSuggestions", "personEditor", matches => {
  const name = $("personName").value.trim().toLowerCase();
  const owner = matches.find(p => p.id !== state.editing?.id && p.name.toLowerCase() === name);
  $("personNameHint").textContent = owner
    ? `Saving merges this group into ${owner.name}.`
    : "An existing name combines this group with that person.";
});
async function editPerson(p) {
  state.editing = p;
  mergeSelection.clear();
  updateMergeSelection();
  $("mergeResults").replaceChildren();
  $("singleFaceTarget").hidden = !p.face_id;
  $("faceCorrection").hidden = !p.face_id;
  $("undoMerge").hidden = true;
  $("undoNote").hidden = true;
  loadMergeUndo(p);
  $("personName").value = p.name || "";
  personNameAutocomplete.reset();
  $("editError").textContent = "";
  $("mergeOutcome").hidden=true;$("mergeOutcome").replaceChildren();
  $("mergeFooterOutcome").hidden=true;$("mergeFooterOutcome").textContent="";
  $("mergeSearch").value = "";
  modal("personEditor");
  $("personName").focus();
  personNameAutocomplete.load();
  if(p.face_id) mergeOptions();
  $("manualMergeSearch").open = !!p.face_id;
  loadPersonSuggestions(p);
}
$("mergeSearch").oninput = () => {
  clearTimeout(mergeTimer);
  mergeTimer = setTimeout(mergeOptions, 250);
};
$("personForm").onsubmit = async (e) => {
  e.preventDefault();
  if ($("personForm").getAttribute("aria-busy") === "true") return;
  const button = $("personForm").querySelector("[type=submit]");
  button.disabled = true;
  $("personForm").setAttribute("aria-busy", "true");
  personNameAutocomplete.cancel();
  try {
    const name = $("personName").value.trim();
    const result = await api("/api/people/" + state.editing.id, { name, match_existing: true });
    if (result.merged_into) {
      mergePerson(state.editing, {id: result.merged_into, name: result.name});
      mergeGridDirty = true;
    } else {
      renamePerson(state.editing.id, result.name);
    }
    closeModal("personEditor");
    toast(result.merged_into ? `Group merged into ${result.name}` : "Person name saved");
    refreshStatus();
  } catch (e) {
    $("editError").textContent = e.message;
  } finally {
    button.disabled = false;
    $("personForm").removeAttribute("aria-busy");
  }
};
$("mergePerson").onclick = () => {
  if (!mergeSelection.size) return;
  $("mergeKeepName").textContent = `Keep ${state.editing.name || "Person " + state.editing.id} and add these ${mergeSelection.size} groups:`;
  $("mergeConfirmGroups").replaceChildren();
  for (const person of mergeSelection.values()) {
    const row = mergeGroupRow(person);
    row.querySelector(".merge-choice").remove();
    $("mergeConfirmGroups").append(row);
  }
  $("mergeConfirmError").textContent = "";
  modal("mergeConfirm");
  renderMergeImageCount();
};
$("cancelMergeConfirm").onclick = () => closeModal("mergeConfirm");
$("confirmMergeGroups").onclick = async () => {
  const button = $("confirmMergeGroups");
  button.disabled = true;
  const originalLabel=button.textContent;
  button.textContent=`Merging ${mergeSelection.size} groups...`;
  $("mergeConfirm").setAttribute("aria-busy","true");
  $("cancelMergeConfirm").disabled = true;
  try {
    const result = await api(`/api/people/${state.editing.id}/merge-many`, { sources: [...mergeSelection.keys()] });
    const mergedIds=new Set([...(result.merged_ids||[]),...(result.already_merged_ids||[])]);
    if (state.person && mergedIds.has(state.person.id)) state.person = { ...state.editing };
    const handledIds=new Set([...mergedIds,...(result.failed||[]).map(p=>p.id)]);
    closeModal("mergeConfirm");
    mergeSelection.clear();
    for(const id of mergedIds) document.querySelector(`.person-card[data-person="${id}"]`)?.remove();
    mergeGridDirty=true;
    $("mergeResults").replaceChildren();
    state.mergeCandidates=[];
    refreshStatus();loadMergeUndo(state.editing);
    const removedFromShown=suggestionPool.slice(0,suggestionShown).filter(p=>handledIds.has(p.id)).length;
    suggestionPool=suggestionPool.filter(p=>!handledIds.has(p.id));
    suggestionShown-=removedFromShown;
    suggestedGroups=suggestedGroups.filter(p=>!handledIds.has(p.id));
    for(const input of document.querySelectorAll("#personSuggestions input[data-merge-group]")) if(handledIds.has(Number(input.dataset.mergeGroup))) input.closest(".face-suggestion").remove();
    $("refreshSuggestions").hidden=false;
    updateSuggestionProgress();updateMergeSelection();
    const failed=result.failed||[];
    const summary=`${result.merged} groups merged${result.already_merged?`, ${result.already_merged} already merged`:""}${failed.length?`, ${failed.length} skipped`:""}.`;
    const outcome=$("mergeOutcome");outcome.hidden=false;outcome.replaceChildren(text("strong",summary));
    $("mergeFooterOutcome").textContent=summary;$("mergeFooterOutcome").hidden=false;
    if(failed.length){
      const list=document.createElement("ul");
      for(const group of failed){const item=document.createElement("li");item.append(text("span",`${group.name?group.name+" (group "+group.id+")":"Person "+group.id}: ${group.reason}`));if(group.current_person_id){const link=text("a"," Open current group");link.href=`/?person=${group.current_person_id}`;link.target="_blank";link.rel="noopener";item.append(link);}list.append(item);}
      const details=document.createElement("details");details.open=true;details.append(text("summary","Skipped groups and reasons"),list);outcome.append(details);
      outcome.scrollIntoView({block:"center"});
    }
    $("mergeResultTitle").textContent=failed.length ? "Merge finished with skipped groups" : result.merged ? "Merge complete" : "Groups already merged";
    $("mergeResultPerson").textContent=`Into ${state.editing.name || "Person " + state.editing.id}`;
    $("mergeResultDetails").replaceChildren(...[...outcome.childNodes].map(node=>node.cloneNode(true)));
    modal("mergeResult");
    $("finishMergeReview").focus();
  } catch (e) { $("mergeConfirmError").textContent = e.message; }
  finally { button.disabled = false;button.textContent=originalLabel;$("mergeConfirm").setAttribute("aria-busy","false"); $("cancelMergeConfirm").disabled = false; }
};
$("mergeConfirm").addEventListener("cancel", e => {
  if ($("confirmMergeGroups").disabled) e.preventDefault();
});
$("cancelPerson").onclick = $("doneMergeReview").onclick = () => closeModal("personEditor");
$("continueMergeReview").onclick = () => closeModal("mergeResult");
$("finishMergeReview").onclick = () => {closeModal("mergeResult");closeModal("personEditor");};
$("personEditor").addEventListener("close",()=>{
  suggestionController?.abort();
  suggestionGeneration++;
  if(mergeGridDirty){mergeGridDirty=false;void load();}
});
async function refreshStatus() {
  try {
    const s = await api("/api/indexing");
    state.status = s;
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
  $("viewerFavorite").textContent = a.favorite ? "\u2665" : "\u2661";
  $("viewerFavorite").setAttribute("aria-pressed", String(!!a.favorite));
  $("viewerFavorite").title = a.favorite ? "Remove from favorites" : "Add to favorites";
  $("viewerFavorite").setAttribute("aria-label", $("viewerFavorite").title);
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
// Phone backups append a content hash to the original basename.
function displayFilename(name = "") {
  return name.replace(/--[a-f0-9]{64}(?=\.[^.]+$)/i, "");
}
// Measure the target photo, including during prefetch. The current image may
// have a different orientation or still be showing its thumbnail.
function viewerSize(photo) {
  const stage = document.querySelector(".viewer-stage");
  const ratio = (photo.width || 4) / (photo.height || 3);
  const width = Math.min(Math.max(1, (stage.clientWidth || innerWidth)),
    Math.max(1, stage.clientHeight || innerHeight * .7) * ratio);
  return { width, height: width / ratio };
}
const viewerURL = (photo) => {
  const size = viewerSize(photo);
  const pixels = Math.max(size.width, size.height) * Math.min(devicePixelRatio || 1, 2) * viewerZoom;
  const side = [1280, 1920, 2560].find(side => side >= pixels) || 2560;
  return photo.image_url + `?width=${side}&height=${side}&quality=80`;
};
let viewerFetch = null;
let viewerZoom = 1, viewerPanX = 0, viewerPanY = 0, viewerDrag = null;
function setViewerZoom(value) {
  viewerZoom = Math.max(1, Math.min(4, value));
  const image = $("fullImage");
  const maxX = image.clientWidth * (viewerZoom - 1) / 2;
  const maxY = image.clientHeight * (viewerZoom - 1) / 2;
  viewerPanX = Math.max(-maxX, Math.min(maxX, viewerPanX));
  viewerPanY = Math.max(-maxY, Math.min(maxY, viewerPanY));
  image.style.transform = `translate(${viewerPanX}px, ${viewerPanY}px) scale(${viewerZoom})`;
  $("viewer").classList.toggle("zoomed", viewerZoom > 1);
  $("zoomReset").textContent = `${Math.round(viewerZoom * 100)}%`;
  $("zoomOut").disabled = viewerZoom <= 1;
  $("zoomIn").disabled = viewerZoom >= 4;
  placeFaceLayer();
}
$("zoomIn").onclick = () => {setViewerZoom(viewerZoom + .25); upgradeViewerResolution();};
$("zoomOut").onclick = () => setViewerZoom(viewerZoom - .25);
$("zoomReset").onclick = () => setViewerZoom(1);
$("fullImage").draggable = false;
$("fullImage").addEventListener("pointerdown", e => {
  if (viewerZoom <= 1 || e.button !== 0) return;
  viewerDrag = {x:e.clientX, y:e.clientY, panX:viewerPanX, panY:viewerPanY};
  e.currentTarget.setPointerCapture(e.pointerId);e.preventDefault();
});
$("fullImage").addEventListener("pointermove", e => {
  if (!viewerDrag) return;
  viewerPanX = viewerDrag.panX + e.clientX - viewerDrag.x;
  viewerPanY = viewerDrag.panY + e.clientY - viewerDrag.y;
  setViewerZoom(viewerZoom);
});
for (const event of ["pointerup", "pointercancel", "lostpointercapture"]) $("fullImage").addEventListener(event, () => {viewerDrag=null;});
function fetchViewerImage(image, url) {
  image.dataset.requestedResolution = url;
  viewerFetch?.abort();
  const controller = new AbortController();
  viewerFetch = controller;
  const cached = clientThumbnails.get(url);
  if (cached && cached.expires > Date.now()) { image.src = cached.src; return; }
  // A failed/stalled response must leave a retryable preview, not an endless spinner.
  const timeout = setTimeout(() => {
    if (!controller.signal.aborted) { controller.abort(); image.onerror?.(); }
  }, 30000);
  fetch(url + "&priority=viewer", { signal: controller.signal, priority: "high" })
    .then(r => { if (!r.ok) throw Error("Preview " + r.status); return r.blob(); })
    .then(blob => {
      if (!controller.signal.aborted) image.src = cacheThumbnail(url, blob).src;
    })
    .catch(() => { if (!controller.signal.aborted) image.onerror?.(); })
    .finally(() => clearTimeout(timeout));
}
let resolutionTimer;
function upgradeViewerResolution() {
  clearTimeout(resolutionTimer);
  resolutionTimer = setTimeout(() => {
    const photo = currentPhoto();
    if (!photo || !$("viewer").open) return;
    const url = viewerURL(photo), display = $("fullImage");
    if (display.dataset.resolution === url || viewerRequest?.dataset.requestedResolution === url) return;
    const gen = state.viewerGeneration, image = new Image();
    viewerRequest = image;
    image.onload = () => {
      if (gen !== state.viewerGeneration || viewerRequest !== image) return;
      display.src = image.src;
      display.dataset.resolution = url;
      display.classList.remove("loading");
      preloadNext(photo, gen, !!state.playbackPhoto);
    };
    image.onerror = () => {
      if (gen !== state.viewerGeneration || viewerRequest !== image) return;
      viewerRequest = null;
      display.classList.remove("loading");
      toast("Full preview unavailable. Try zooming again or open the original.");
    };
    fetchViewerImage(image, url);
  }, 160);
}
document.querySelector(".viewer-stage").addEventListener("wheel",e=>{
  if(!$("viewer").open)return;e.preventDefault();stopPlay();
  setViewerZoom(viewerZoom*Math.exp(-e.deltaY*(e.deltaMode===1?.04:.002)));
  if(viewerZoom>1)upgradeViewerResolution();
},{passive:false});
let viewerRequest = null, nextPreview = null, playbackPrefetch = null;
function playbackQuery(previous) {
  const p = params();
  p.set("order", state.shuffle ? "random" : "sequential");
  if (previous) p.set("after", previous.id);
  return "/api/playback/next?" + p;
}
function clearViewerPreview(keepURL = null) {
  clearTimeout(resolutionTimer);
  viewerFetch?.abort();
  viewerFetch = null;
  if (viewerRequest) {
    viewerRequest.onload = viewerRequest.onerror = null;
    viewerRequest.removeAttribute("src");
  }
  viewerRequest = null;
  if (nextPreview && nextPreview.url !== keepURL) {
    nextPreview.controller.abort();
    nextPreview = null;
  }
  playbackPrefetch = null;
}
function preloadNext(photo, gen, external) {
  if (gen !== state.viewerGeneration || !$("viewer").open) return;
  const warm = (next) => {
    if (!next || next.format === "VIDEO" || next.id === photo.id || gen !== state.viewerGeneration || !$("viewer").open) return;
    const url = viewerURL(next);
    if (nextPreview?.url === url) return;
    nextPreview?.controller.abort();
    nextPreview = null;
    const cached = clientThumbnails.get(url);
    if (cached && cached.expires > Date.now()) return;
    const controller = new AbortController(), request = { url, controller };
    nextPreview = request;
    const timeout = setTimeout(() => controller.abort(), 30000);
    fetch(url + "&priority=prefetch", { signal: controller.signal, priority: "low" })
      .then(r => { if (!r.ok) throw Error("Preview " + r.status); return r.blob(); })
      .then(blob => { if (!controller.signal.aborted) cacheThumbnail(url, blob); })
      .catch(() => {})
      .finally(() => {
        clearTimeout(timeout);
        if (nextPreview === request) nextPreview = null;
      });
  };
  if (external) {
    const url = playbackQuery(photo);
    const promise = api(url).catch(() => null);
    playbackPrefetch = { url, promise };
    promise.then(warm);
  } else {
    const index = state.shuffle && state.shuffleStarted
      ? state.bag[state.bag.length - 1] : state.index + 1;
    warm(state.items[index]);
  }
}
function showViewerPreview(photo, gen, external) {
  sizeViewerImage(photo);
  const url = viewerURL(photo);
  // Retain a matching in-flight prefetch. The foreground request promotes its
  // shared server render and both use the same client cache key.
  clearViewerPreview(url);
  const display = $("fullImage");
  display.classList.add("loading");
  display.style.visibility = "hidden";
  display.removeAttribute("src");
  display.onload = () => {
    if (gen === state.viewerGeneration) display.style.visibility = "visible";
  };
  display.onerror = () => {
    if (gen === state.viewerGeneration) display.style.visibility = "hidden";
  };
  const thumbURL = "/api/images/" + photo.id + "/thumbnail";
  display.src = clientThumbnails.get(thumbURL)?.src || thumbURL;
  const full = new Image();
  viewerRequest = full;
  full.onload = () => {
    if (gen !== state.viewerGeneration || viewerRequest !== full) return;
    // Record the variant actually loaded, never a URL recomputed after layout.
    display.dataset.resolution = url;
    display.src = full.src;
    display.classList.remove("loading");
    preloadNext(photo, gen, external);
    if (viewerURL(photo) !== url) upgradeViewerResolution();
  };
  full.onerror = () => {
    if (gen !== state.viewerGeneration || viewerRequest !== full) return;
    viewerRequest = null;
    display.classList.remove("loading");
    toast("Full preview unavailable. Showing thumbnail; try the original.");
  };
  fetchViewerImage(full, url);
}
async function openImage(i, external = null) {
  if (!state.items.length && !external) return;
  state.playbackPhoto = external;
  if (!external) state.index = (i + state.items.length) % state.items.length;
  const gen = ++state.viewerGeneration;
  const photo = currentPhoto();
  setViewerZoom(1);
  delete $("fullImage").dataset.resolution;
  const a = state.annotations[photo.id] || {};
  $("fullImage").alt = a.title || displayFilename(photo.filename);
  modal("viewer");
  const video = $("fullVideo"), isVideo = photo.format === "VIDEO";
  video.onerror = video.onloadedmetadata = null; video.pause(); video.removeAttribute("src"); video.load();
  video.hidden = !isVideo; $("fullImage").hidden = isVideo;
  if (isVideo) {
    if (state.playing) stopPlay();
    clearViewerPreview();
    let converted = false;
    const convertVideo = () => {
      if (gen !== state.viewerGeneration || !$("viewer").open) return;
      if (converted) { toast("Video playback failed. Install FFmpeg with H.264 support or open the original."); return; }
      converted = true;
      toast("Preparing a compatible video stream…");
      video.src = "/api/images/" + photo.id + "/video";
      video.load();
      video.play().catch(() => {});
    };
    video.onerror = convertVideo;
    video.onloadedmetadata = () => { if (!converted && !video.videoWidth) convertVideo(); };
    video.src = photo.original_url;
    video.load();
    preloadNext(photo, gen, external);
  } else showViewerPreview(photo, gen, external);
  $("viewerBack").textContent="\u2190";
  $("imagePath").textContent = photo.path || [photo.directory, photo.filename].filter(Boolean).join("/");
  $("imagePath").title = $("imagePath").textContent;
  $("imageMeta").textContent = [
    date(photo),
    a.location || a.auto_location,
    photo.width && photo.height ? `${photo.width} × ${photo.height}` : (photo.format === "VIDEO" ? "Video" : ""),
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
const faceName = (f) => f.name || "Person " + f.person_id;
const faceNameAutocomplete = nameAutocomplete("faceName", "faceSuggestions", "faceEditor", matches => {
  state.faceMatches = matches;
  faceHint();
});
state.faces = [];
state.faceMap = {};
state.showFaces = localStorage.getItem("showFaces") !== "false";
syncFaceToggles();
async function loadImageFaces(photo, gen) {
  facesController?.abort();
  const controller = typeof AbortController === "function" ? new AbortController() : null;
  facesController = controller;
  state.faces = state.faceMap[photo.id] || [];
  renderFaces();
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
    state.faces = faces;
    if (photo.id in state.faceMap) {
      state.faceMap[photo.id] = faces;
      refreshThumbnailFaces(photo.id);
    }
    renderFaces();
  } catch (e) {
    /* Photos remain usable while AI is unavailable. */
  }
}
// Face tags for the visible page, fetched after the gallery renders.
async function loadThumbnailFaces(gen, items = state.items) {
  const ids = items.map((p) => p.id);
  if (!ids.length) return;
  try {
    const faces = await api(
      "/api/faces" + (state.view === "hidden" ? "?hidden=only" : ""),
      { ids },
    );
    if (gen !== state.generation) return;
    for (const id of ids) state.faceMap[id] = faces[id] || [];
    refreshThumbnailFaces();
  } catch (e) {
    /* Thumbnails remain usable while AI is unavailable. */
  }
}
function refreshThumbnailFaces(only) {
  for (const card of document.querySelectorAll(".photo-card")) {
    if (only && card.dataset.id !== only) continue;
    thumbnailFaces(card, card.querySelector(".photo-open img"));
  }
}
// Place a face box, given in ML preview coordinates, over an image shown in a
// box of `ratio` (width / height) with object-fit: cover (thumbnails) or contain.
function faceBox(f, onClick, ratio) {
  const box = document.createElement("button");
  box.className = "face-box" + (f.name ? "" : " unnamed");
  box.type = "button";
  const r = f.width / f.height;
  let w = 1,
    h = 1;
  if (ratio && r > ratio) w = r / ratio;
  else if (ratio) h = ratio / r;
  const x = (1 - w) / 2,
    y = (1 - h) / 2;
  box.style.left = (x + (f.box.x1 / f.width) * w) * 100 + "%";
  box.style.top = (y + (f.box.y1 / f.height) * h) * 100 + "%";
  box.style.width = ((f.box.x2 - f.box.x1) / f.width) * w * 100 + "%";
  box.style.height = ((f.box.y2 - f.box.y1) / f.height) * h * 100 + "%";
  box.setAttribute("aria-label", "Tag " + faceName(f));
  box.title = faceName(f);
  box.append(text("span", faceName(f), "face-label"));
  box.onclick = (e) => {
    e.preventDefault();
    e.stopPropagation();
    onClick();
  };
  return box;
}
function thumbnailFaces(card, img) {
  card.querySelector(".thumb-faces")?.remove();
  const faces = state.faceMap[card.dataset.id];
  if (!state.showFaces || !faces?.length) return;
  const layer = text("div", "", "thumb-faces");
  for (const f of faces)
    layer.append(faceBox(f, () => editFace(f, img, card.dataset.id), 4 / 3));
  card.querySelector(".photo-open").after(layer);
}
function renderFaces() {
  const layer = $("faceLayer");
  layer.replaceChildren();
  $("imagePeople").replaceChildren();
  syncFaceToggles();
  const photo = currentPhoto();
  for (const f of state.faces) {
    const open = () => {
      stopPlay();
      editFace(f, $("fullImage"), photo?.id);
    };
    const box = faceBox(f, open);
    const chip = text("button", f.name || "Name this face");
    chip.onclick = open;
    for (const e of [box, chip]) {
      e.onmouseenter = () => {
        box.classList.add("active");
        chip.classList.add("active");
      };
      e.onmouseleave = () => {
        box.classList.remove("active");
        chip.classList.remove("active");
      };
    }
    layer.append(box);
    $("imagePeople").append(chip);
  }
  placeFaceLayer();
}
// The image is centered with object-fit: contain, so match its rendered box.
function placeFaceLayer() {
  const layer = $("faceLayer"),
    img = $("fullImage");
  layer.hidden =
    !state.showFaces || !state.faces.length || !img.complete || !img.naturalWidth;
  if (layer.hidden) return;
  const stage = img.parentElement.getBoundingClientRect(),
    r = img.getBoundingClientRect();
  layer.style.left = r.left - stage.left + "px";
  layer.style.top = r.top - stage.top + "px";
  layer.style.width = r.width + "px";
  layer.style.height = r.height + "px";
}
$("fullImage").addEventListener("load", placeFaceLayer);
function sizeViewerImage(photo = currentPhoto()) {
  if (!photo) return;
  const size = viewerSize(photo), image = $("fullImage");
  image.style.width = size.width + "px";
  image.style.height = size.height + "px";
}
window.addEventListener("resize",()=>{if($("viewer").open){sizeViewerImage();upgradeViewerResolution();}placeFaceLayer();});
if (typeof ResizeObserver === "function")
  new ResizeObserver(placeFaceLayer).observe($("fullImage"));
function syncFaceToggles() {
  for(const id of ["toggleFaces","toggleGridFaces"]){
    if (id === "toggleGridFaces") $(id).textContent=state.showFaces?"Face tags on":"Face tags off";
    $(id).setAttribute("aria-pressed",String(state.showFaces));
    $(id).setAttribute("aria-label",state.showFaces?"Hide face tags":"Show face tags");
  }
  $("imagePeople").hidden=!state.showFaces;
}
function toggleFaceTags(){
  state.showFaces=!state.showFaces;
  localStorage.setItem("showFaces",String(state.showFaces));
  syncFaceToggles();renderFaces();refreshThumbnailFaces();
}
$("toggleFaces").onclick=toggleFaceTags;
$("toggleGridFaces").onclick=toggleFaceTags;

// Apply a tag change to every loaded face list, the People grid and open views
// in place, without reloading the page of results.
function eachFace(fn) {
  const lists = [state.faces, ...Object.values(state.faceMap)];
  for (const list of new Set(lists)) for (const f of list) fn(f);
}
function facesChanged() {
  if ($("viewer").open) renderFaces();
  refreshThumbnailFaces();
}
function renamePerson(id, name) {
  eachFace((f) => {
    if (f.person_id === id) f.name = name;
  });
  const p = state.people.find((p) => p.id === id);
  if (p) {
    p.name = name;
    updatePersonCard(p);
  }
  if (state.person?.id === id) {
    state.person.name = name;
    personChip();
  }
  facesChanged();
}
function mergePerson(source, target) {
  eachFace((f) => {
    if (f.person_id === source.id) {
      f.person_id = target.id;
      f.name = target.name;
    }
  });
  removePersonCard(source.id);
  void refreshPersonCounts();
  if (state.person?.id === source.id) {
    state.person = { id: target.id, name: target.name };
    personChip();
    syncURL();
  }
  facesChanged();
}
function drawFaceCrop(f, img) {
  const canvas = $("faceCrop"),
    ctx = canvas.getContext("2d");
  ctx.clearRect(0, 0, canvas.width, canvas.height);
  if (!img?.naturalWidth) return;
  const sx = img.naturalWidth / f.width,
    sy = img.naturalHeight / f.height,
    w = (f.box.x2 - f.box.x1) * sx,
    h = (f.box.y2 - f.box.y1) * sy,
    side = Math.max(w, h) * 1.44,
    cx = ((f.box.x1 + f.box.x2) / 2) * sx,
    cy = ((f.box.y1 + f.box.y2) / 2) * sy;
  ctx.drawImage(img, cx - side / 2, cy - side / 2, side, side, 0, 0, canvas.width, canvas.height);
}
// The existing group, other than this face's own, already using the typed name.
function faceNameOwner() {
  const name = $("faceName").value.trim().toLowerCase();
  return (state.faceMatches || []).find(
    (p) => p.name.toLowerCase() === name && p.id !== state.editingFace.person_id,
  );
}
function faceHint() {
  const owner = faceNameOwner(),
    scope = document.querySelector("input[name=faceScope]:checked").value;
  $("faceHint").textContent = owner
    ? scope === "group"
      ? `${owner.name} already exists. Saving merges ${faceName(state.editingFace)} into ${owner.name}.`
      : `This face moves to ${owner.name}.`
    : scope === "face" && $("faceName").value.trim()
      ? "This face starts a new person."
      : scope === "group" && $("faceName").value.trim()
        ? "An existing name combines this group with that person."
        : "";
}
function editFace(f, img, imageId) {
  state.editingFace = f;
  state.editingFaceImage = imageId;
  state.faceMatches = [];
  $("faceSuggestions").replaceChildren();
  $("faceName").value = f.name || "";
  faceNameAutocomplete.reset();
  $("faceError").textContent = "";
  $("faceHint").textContent = "";
  $("faceScopeGroup").textContent = "Every photo of " + faceName(f);
  document.querySelector("input[name=faceScope][value=group]").checked = true;
  drawFaceCrop(f, img);
  modal("faceEditor");
  $("faceName").focus();
  $("faceName").select();
  faceNameAutocomplete.load();
}
for (const r of document.querySelectorAll("input[name=faceScope]"))
  r.onchange = faceHint;
$("cancelFace").onclick = () => closeModal("faceEditor");
$("faceForm").onsubmit = async (e) => {
  e.preventDefault();
  if ($("faceForm").getAttribute("aria-busy") === "true") return;
  const f = state.editingFace,
    name = $("faceName").value.trim(),
    scope = document.querySelector("input[name=faceScope]:checked").value;
  const button = e.submitter || $("faceForm").querySelector("[type=submit]");
  const label = button.textContent;
  button.disabled = true;
  button.textContent = "Saving…";
  $("faceForm").setAttribute("aria-busy", "true");
  $("faceError").textContent = "";
  faceNameAutocomplete.cancel();
  try {
    let message;
    if (scope === "face") {
      if (!name) throw Error("Enter a name for this face.");
      const moved = await api("/api/faces/" + f.id, { name });
      eachFace((x) => {
        if (x.id === f.id) {
          x.person_id = moved.person_id;
          x.name = moved.name;
        }
      });
      facesChanged();
      message = "Face tagged as " + moved.name;
    } else {
      const result = await api("/api/people/" + f.person_id, { name, match_existing: true });
      if (result.merged_into) {
        const source = state.people.find((p) => p.id === f.person_id) || { id: f.person_id };
        mergePerson(source, { id: result.merged_into, name: result.name });
        message = `Tagged as ${result.name}`;
      } else {
        renamePerson(f.person_id, result.name);
        message = result.name ? "Tagged as " + result.name : "Name cleared";
      }
    }
    closeModal("faceEditor");
    toast(message);
    refreshStatus();
  } catch (e) {
    $("faceError").textContent = e.message;
  } finally {
    button.disabled = false;
    button.textContent = label;
    $("faceForm").removeAttribute("aria-busy");
  }
};
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
  $("play").textContent = "\u25b6";
  $("play").title = "Play slideshow";
  $("play").setAttribute("aria-label", $("play").title);
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
  $("play").textContent = "\u275a\u275a";
  $("play").title = "Pause slideshow";
  $("play").setAttribute("aria-label", $("play").title);
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
    const cap = state.semantic && !state.grouped ? Math.min(state.total, 1060) : state.total;
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
  $("fullVideo").onerror = $("fullVideo").onloadedmetadata = null; $("fullVideo").pause(); $("fullVideo").removeAttribute("src"); $("fullVideo").load();
  clearViewerPreview();
  pumpThumbnails();
  const display = $("fullImage");
  display.onload = display.onerror = null;
  display.style.visibility = "hidden";
  display.removeAttribute("src");
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
$("viewerBack").onclick = () => $("closeViewer").click();
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
  $("shuffle").setAttribute("aria-pressed", String(state.shuffle));
};
document.addEventListener("fullscreenchange", () => {
  $("fullscreen").setAttribute("aria-pressed", String(!!document.fullscreenElement));
  $("fullscreen").setAttribute("aria-label", document.fullscreenElement ? "Exit fullscreen" : "Enter fullscreen");
  if (document.fullscreenElement && $("viewer").open) upgradeViewerResolution();
});
$("fullscreen").onclick = async () => {
  try {
    if (document.fullscreenElement) await document.exitFullscreen();
    else if (document.documentElement.requestFullscreen)
      await document.documentElement.requestFullscreen();
    else toast("Fullscreen unavailable here. Press F11 to enter browser fullscreen.");
  } catch (e) {
    toast("Fullscreen unavailable here. Press F11 to enter browser fullscreen.");
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
      $("detailStatus").textContent = "Saved";
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

let personFilterMatches = [], personFilterTimer;
$("personFilter").addEventListener("input", () => {
  clearTimeout(personFilterTimer);
  const query = $("personFilter").value.trim();
  personFilterTimer = setTimeout(async () => {
    try { const result = await api("/api/people/names?limit=50&q=" + encodeURIComponent(query));
      if ($("personFilter").value.trim() !== query) return;
      personFilterMatches = result.items;
      $("personFilterSuggestions").replaceChildren(...result.items.map(p => new Option(p.name)));
    } catch (e) { toast(e.message); }
  }, 200);
});
$("addPersonFilter").onclick = async () => {
  const name = $("personFilter").value.trim();
  if (!name) return;
  try {
    const result = await api("/api/people/names?limit=200&q=" + encodeURIComponent(name));
    const matches = result.items.filter(p => p.name.toLowerCase() === name.toLowerCase());
    if (matches.length !== 1) { toast(matches.length ? "Choose a unique person name" : "Choose a person from the suggestions"); return; }
    const person = matches[0];
    if (!state.peopleFilters.some(p => p.id === person.id)) state.peopleFilters.push(person);
    $("personFilter").value = ""; state.offset = 0; load();
  } catch (e) { toast(e.message); }
};

$("applyFilters").onclick = () => {
  state.offset = 0;
  state.savedName = "";
  load();
};
$("resetFilters").onclick = () => {
  for (const id of Object.values(fields)) $(id).value = "";
  state.album = null;
  state.peopleFilters = [];
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
for (const b of document.querySelectorAll("[data-view]"))
  b.onclick = () => setView(b.dataset.view);
$("clearPerson").onclick = () => {
  state.person = null;
  state.offset = 0;
  load();
};
$("refresh").onclick = () => {
  gridPages.clear();
  load(true);
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
$("next").onclick = () => loadMoreGrid();
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
  if (
    !$("viewer").open ||
    $("faceEditor").open ||
    /INPUT|TEXTAREA|SELECT/.test(e.target.tagName)
  )
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
  void loadAlbums().catch((e) => toast(e.message));
  if (state.album)
    state.album =
      state.albums.find((a) => a.id === state.album.id) || state.album;
  if (p.get("order") === "random") {
    state.shuffle = true;
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
    const url = playbackQuery(previous);
    const prefetched = playbackPrefetch?.url === url ? await playbackPrefetch.promise : null;
    const next = prefetched || await api(url, undefined, controller?.signal);
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
const themeQuery = matchMedia("(prefers-color-scheme: dark)");
function applyTheme(theme) {
  document.documentElement.dataset.theme = theme;
  document.querySelector('meta[name="theme-color"]').content =
    theme === "dark" ? "#101115" : "#f6f6f8";
  const label = `Switch to ${theme === "dark" ? "light" : "dark"} theme`;
  $("themeToggle").setAttribute("aria-label", label);
  $("themeToggle").title = label;
}
applyTheme(document.documentElement.dataset.theme);
$("themeToggle").onclick = () => {
  const next =
    document.documentElement.dataset.theme === "dark" ? "light" : "dark";
  try {
    localStorage.setItem("homeImages.theme", next);
  } catch {}
  applyTheme(next);
};
themeQuery.addEventListener("change", (event) => {
  let stored = null;
  try {
    stored = localStorage.getItem("homeImages.theme");
  } catch {}
  if (!stored) applyTheme(event.matches ? "dark" : "light");
});
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

function appendSuggestionPage() {
  const person = state.editing;
  const container = $("personSuggestions");
  const batch = suggestionPool.slice(suggestionShown, suggestionShown + 25);
  suggestionShown += batch.length;
  for (const candidate of batch) {
    if (suggestedGroups.some(p => p.id === candidate.id)) continue;
    suggestedGroups.push(candidate);
    const row = mergeGroupRow(candidate);
    const name = row.querySelector("strong");
    const link = text("a", name.textContent);
    link.href=`/?person=${candidate.id}`;link.target="_blank";link.rel="noopener";name.replaceWith(link);
    const score = text("small",`${Math.round(candidate.similarity*100)}% similarity`);
    row.querySelector(".merge-card-info").append(score);
    const dismiss=text("button","Not a match","text-button dismiss-match");
    dismiss.setAttribute("aria-label", `${candidate.name || "Person " + candidate.id} is not the same person`);
    dismiss.onclick=async()=>{
      dismiss.disabled=true;
      try {await api(`/api/people/${person.id}/dismiss`,{other:candidate.id});
        if(state.editing.id!==person.id)return;
        mergeSelection.delete(candidate.id);suggestedGroups=suggestedGroups.filter(p=>p.id!==candidate.id);row.remove();updateMergeSelection();loadMergeUndo(person);
      }catch(e){$("editError").textContent=e.message;dismiss.disabled=false;}
    };
    row.append(dismiss);container.append(row);
  }
  updateSuggestionProgress();
  updateMergeSelection();
}
function updateSuggestionProgress(){
  $("moreSuggestions").hidden=suggestionShown>=suggestionPool.length;
  $("suggestionProgress").textContent=`Showing ${suggestionShown} ranked groups. Select matches, then show more until the choices no longer look right. Up to 5,000 groups per merge.`;
}
$("refreshSuggestions").onclick=()=>loadPersonSuggestions(state.editing);
$("moreSuggestions").onclick=appendSuggestionPage;
let suggestionController;
async function loadPersonSuggestions(person) {
  suggestionController?.abort();
  const controller = suggestionController = new AbortController();
  $("reviewPortrait").src=person.image_url||`/api/people/${person.id}/image`;
  const container=$("personSuggestions");
  const generation=++suggestionGeneration;
  suggestedGroups=[];suggestionPool=[];suggestionShown=0;
  $("refreshSuggestions").hidden=true;
  $("moreSuggestions").hidden=true;$("suggestionProgress").textContent="";updateMergeSelection();
  container.setAttribute("aria-busy","true");
  const loading=text("div","","suggestions-loading");loading.setAttribute("role","status");
  const spinner=text("span","","loading-spinner");spinner.setAttribute("aria-hidden","true");
  loading.append(spinner,text("span","Searching for similar groups..."));container.replaceChildren(loading);
  try {
    // One ranked snapshot avoids shifting pages while indexing/dismissals run.
    // Reveal 25 at a time; only revealed portraits are requested.
    const result=await api(`/api/people/${person.id}/suggestions?limit=1000&min_score=0`, undefined, controller.signal);
    if(state.editing.id!==person.id||generation!==suggestionGeneration)return;
    suggestionPool=result.items;container.replaceChildren();
    if(!suggestionPool.length)container.textContent="No more groups to review. You can search by name below.";
    appendSuggestionPage();
  }catch(e){if(e.name!=="AbortError"&&state.editing.id===person.id&&generation===suggestionGeneration)container.textContent=e.message;}
  finally {if(generation===suggestionGeneration)container.setAttribute("aria-busy","false");}
}

function confirmPersonAction(message) {
  return new Promise(resolve => {
    const dialog = $("personActionConfirm");
    $("personActionMessage").textContent = message;
    let accepted = false;
    $("personActionOK").onclick = () => { accepted = true; closeModal("personActionConfirm"); };
    $("personActionCancel").onclick = () => closeModal("personActionConfirm");
    dialog.addEventListener("close", () => resolve(accepted), { once: true });
    modal("personActionConfirm");
  });
}
let pendingMergeUndo;
async function loadMergeUndo(person) {
  try {
    const history = await api("/api/people/merge-history");
    if (state.editing.id !== person.id) return;
    pendingMergeUndo = history;
    $("undoMerge").hidden = !history.available;
    $("undoNote").hidden = !history.available;
  } catch(e) { $("editError").textContent=e.message; }
}
$("undoMerge").onclick = async () => {
  if (!pendingMergeUndo?.available || !await confirmPersonAction("Undo the latest merge and restore its original face groups?")) return;
  $("undoMerge").disabled=true;
  try {
    await api("/api/people/undo", {source:pendingMergeUndo.source,target:pendingMergeUndo.target});
    closeModal("personEditor"); await load(); refreshStatus(); toast("Merge undone");
  } catch(e) { $("editError").textContent=e.message; }
  finally { $("undoMerge").disabled=false; }
};
$("moveFace").onclick = async () => {
  const target=Number($("mergeTarget").value);
  if (!target) { $("editError").textContent="Choose the correct person below first.";return; }
  if (!await confirmPersonAction("Move only this face into the selected person?")) return;
  $("moveFace").disabled=true;
  try {
    await api(`/api/faces/${state.editing.face_id}`, {person:target});
    closeModal("personEditor"); await load(); refreshStatus(); toast("Face moved");
  } catch(e) { $("editError").textContent=e.message; }
  finally { $("moveFace").disabled=false; }
};

for (const id of ["photoGroup", "photoSort", "sortDirection"]) $(id).onchange = () => {
  state.offset = 0;
  load();
};
$("listToggle").onclick = () => {
  state.layout = state.layout === "grid" ? "list" : "grid";
  viewChrome();
  syncURL();
};

// Native datalist suggestions preserve free-form filters and keyboard navigation.
for (const [id, kind, listId] of [["tag","tag","tagSuggestions"],["place","place","placeSuggestions"],["directory","folder","folderSuggestions"]]) {
  let timer, controller, generation=0;
  const input=$(id), list=$(listId);
  const suggest=async()=>{
    const gen=++generation;
    controller?.abort();controller=new AbortController();
    input.setAttribute("aria-busy","true");
    try {
      const result=await api("/api/filter-suggestions?"+new URLSearchParams({kind,q:input.value.trim()}),undefined,controller.signal);
      if(gen!==generation)return;
      list.replaceChildren(...result.items.map(value=>new Option(value,value)));
    } catch(error) {if(error.name!=="AbortError" && gen===generation)list.replaceChildren();}
    finally {if(gen===generation)input.setAttribute("aria-busy","false");}
  };
  input.addEventListener("focus",suggest);
  input.addEventListener("input",()=>{clearTimeout(timer);controller?.abort();++generation;list.replaceChildren();timer=setTimeout(suggest,250);});
}

// Append a batch near the bottom; scrolling never replaces already loaded cards.
const moreSentinel = document.createElement("div");
moreSentinel.className = "scroll-sentinel";
moreSentinel.setAttribute("aria-hidden", "true");
$("next").closest(".page-controls").after(moreSentinel);
function maybeLoadMoreGrid() {
  if (!state.moreFailed && moreSentinel.getBoundingClientRect().top < innerHeight + 700 && !$("viewer").open) void loadMoreGrid();
}
const moreObserver = typeof IntersectionObserver === "function" ? new IntersectionObserver(maybeLoadMoreGrid, {rootMargin: "700px"}) : null;
moreObserver?.observe(moreSentinel);
window.addEventListener("scroll", maybeLoadMoreGrid, {passive: true});
