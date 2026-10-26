// Run this expression through the collaborative browser on the isolated preview.
// It deliberately refuses to modify the live library.
(async () => {
  if (location.hostname !== "127.0.0.1" || location.port !== "18374")
    throw Error("Use the isolated preview on 127.0.0.1:18374");
  const results = [];
  const check = (ok, message) => {
    if (!ok) throw Error(message);
    results.push(message);
  };
  const settle = async (predicate) => {
    const deadline = Date.now() + 6000;
    while (!predicate()) {
      if (Date.now() > deadline) throw Error("UI did not settle");
      await new Promise((r) => setTimeout(r, 50));
    }
  };
  const ready = () => !$("gallery").classList.contains("loading");
  const clickView = async (view) => {
    document.querySelector(`[data-view="${view}"]`).click();
    await settle(ready);
  };
  await clickView("photos");
  check(state.items.length === 18, "Library renders 18 actual sample photos");
  const first = state.items[0];
  if (state.annotations[first.id].favorite) await favorite(first.id);
  await document.querySelector(".photo-heart").onclick();
  await clickView("favorites");
  check(
    state.items.length === 1 && state.items[0].id === first.id,
    "Favorite action persists and filters the library",
  );
  await document.querySelector(".photo-open").onclick();
  $("toggleDetails").click();
  check(
    $("viewer").open && !$("detailsPanel").hidden,
    "Photo viewer opens with editable details",
  );
  $("photoTitle").value = "Barcelona afternoon";
  $("photoLocation").value = "Barcelona, Spain";
  $("photoTags").value = "travel, family";
  await $("detailsForm").onsubmit({ preventDefault() {} });
  check(
    $("detailStatus").textContent.includes("Saved"),
    "Title, location and tags save through the real API",
  );
  $("closeViewer").click();
  await clickView("albums");
  $("newAlbum").click();
  $("entryName").value = "UI smoke album";
  await $("nameForm").onsubmit({
    preventDefault() {},
    submitter: document.querySelector("#nameForm button[type=submit]"),
  });
  check(
    document.querySelector(".album-card h3").textContent === "UI smoke album",
    "Album creation is reflected in the grid",
  );
  await clickView("favorites");
  const checkbox = document.querySelector(".photo-select");
  checkbox.checked = true;
  checkbox.dispatchEvent(new Event("change"));
  $("bulkAlbum").value = state.albums[0].id;
  await $("addSelected").onclick();
  await clickView("albums");
  document.querySelector(".album-open").click();
  await settle(ready);
  check(
    state.items.length === 1 && state.items[0].id === first.id,
    "Bulk add and album selection use stored membership",
  );
  $("playbackLinks").click();
  check(
    $("apiURL").value.includes("/api/playback/next?") &&
      $("apiURL").value.includes("album="),
    "Selection exposes a ready-to-use playback API link",
  );
  check(
    $("frameURL").value.includes("frame=true"),
    "Selection exposes a browser frame link",
  );
  $("closePlayback").click();
  $("saveSelection").click();
  $("entryName").value = "UI smoke selection";
  await $("nameForm").onsubmit({
    preventDefault() {},
    submitter: document.querySelector("#nameForm button[type=submit]"),
  });
  await settle(() => document.querySelector(".saved-link"));
  check(
    document.querySelector(".saved-link").textContent === "UI smoke selection",
    "Saved selection appears in navigation",
  );
  $("slideshow").click();
  check(!!state.playing, "Slideshow starts");
  await playbackStep();
  check(
    currentPhoto().id === first.id && !!state.playbackPhoto,
    "Slideshow reads the album through the playback API",
  );
  $("play").click();
  $("closeViewer").click();
  await clickView("photos");
  $("mode").value = "metadata";
  $("query").value = "Barcelona afternoon";
  $("searchForm").dispatchEvent(new Event("submit", { cancelable: true }));
  await settle(ready);
  check(
    state.items.length === 1,
    "Metadata search finds user annotation titles",
  );
  await document.querySelector(".photo-open").onclick();
  $("toggleDetails").click();
  $("photoHidden").checked = true;
  await $("detailsForm").onsubmit({ preventDefault() {} });
  await settle(ready);
  check(
    state.items.length === 0 && !$("viewer").open,
    "Hiding a photo removes it from the current view",
  );
  await clickView("hidden");
  check(state.items.length === 1, "Hidden photos remain available for review");
  const restore = document.querySelector(".photo-select");
  restore.checked = true;
  restore.dispatchEvent(new Event("change"));
  await $("hideSelected").onclick();
  check(
    state.items.length === 0,
    "Hidden photo can be restored with the bulk control",
  );
  await clickView("photos");
  check(state.items.length === 18, "Restored photo returns to the library");
  check(
    document.documentElement.scrollWidth <= innerWidth,
    "Page fits the viewport without horizontal overflow",
  );
  return { passed: results.length, results };
})();
