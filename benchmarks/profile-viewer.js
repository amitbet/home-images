// Run in Chrome DevTools Console on the photo library, with at least five results.
// Performs 50 GETs, at most five concurrently; no annotation/settings writes.
// Browser cache is bypassed, but server cache keys remain unchanged by the nonce.
// Grid qualities vary to approximate cold cache without deleting server files.
// Results contain photo indices, not filenames, IDs, URLs, or photo contents.
// TTFB includes network/server work; it cannot isolate server queue/decoder time.
(async () => {
  const photos = state.items.slice(0, 5);
  if (photos.length !== 5) throw Error("Load at least five photos first");
  const result = {
    recordedAt: new Date().toISOString(),
    health: await (await fetch("/health")).json(),
    rows: [],
  };
  const request = async (index, width, height, quality, label, round) => {
    const url = new URL(photos[index].image_url, location.href);
    url.search = new URLSearchParams({ width, height, quality,
      profile: `${Date.now()}-${Math.random()}` });
    const start = performance.now();
    const response = await fetch(url, {
      cache: "no-store", signal: AbortSignal.timeout(45000),
      priority: label.startsWith("viewer") ? "high" : "low",
    });
    const headers = performance.now();
    const blob = await response.blob();
    const entry = performance.getEntriesByName(url.href).at(-1);
    const row = { index, label, round, width, height, quality,
      status: response.status, ms: Math.round(performance.now() - start),
      headers: Math.round(headers - start),
      ttfb: entry ? Math.round(entry.responseStart - entry.requestStart) : null,
      download: entry ? Math.round(entry.responseEnd - entry.responseStart) : null,
      bytes: blob.size };
    result.rows.push(row);
    console.log("PROFILE_ROW", JSON.stringify(row));
    if (!response.ok) throw Error(`Preview returned ${response.status}`);
  };
  for (let i = 0; i < 5; i++) {
    for (const label of ["dynamic1", "cached1", "dynamic2", "cached2"]) {
      const dynamic = label.startsWith("dynamic");
      await request(i, dynamic ? 1440 : 1280, dynamic ? 960 : 1280, 80, label, i);
    }
  }
  for (let i = 0; i < 5; i++) {
    if (i % 2 === 0) await request(i, 1440, 960, 80, "viewer-alone", i);
    const work = [1, 2, 3, 4].map(j => request((i + j) % 5, 480, 360,
      71 + i * 2, "grid-cold", i));
    await new Promise(resolve => setTimeout(resolve, 30));
    await Promise.all([...work, request(i, 1440, 960, 80, "viewer", i)]);
    if (i % 2 === 1) await request(i, 1440, 960, 80, "viewer-alone", i);
  }
  console.log("PROFILE_DONE", JSON.stringify(result));
  return result;
})();
