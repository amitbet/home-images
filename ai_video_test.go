package main

import (
	"context"
	"encoding/json"
	"image/jpeg"
	"math"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
)

func TestVideoSamplingCoversWholeRecording(t *testing.T) {
	for _, d := range []float64{.1, 1, 25, 3600, 86400} {
		times := videoSampleTimes(d)
		if len(times) < 3 || len(times) > 240 || times[0] < 0 || times[len(times)-1] >= d || times[len(times)-1] < d*.65 {
			t.Fatal(d, times)
		}
		for i := 1; i < len(times); i++ {
			if times[i] <= times[i-1] {
				t.Fatal(times)
			}
		}
	}
	for _, d := range []float64{0, -1, math.NaN(), math.Inf(1)} {
		if len(videoSampleTimes(d)) != 0 {
			t.Fatal(d)
		}
	}
}

func TestVideoIndexPeopleFramesAndFilters(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("FFmpeg required")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("FFprobe required")
	}
	l, s, root := fixture(t)
	ctx := context.Background()
	path := filepath.Join(root, "sample.mp4")
	cmd := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi", "-i", "color=c=red:s=64x96:r=10", "-t", "2", "-c:v", "mpeg4", "-y", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	l.Scan(ctx)
	// Existing photo teaches the video index the same person.
	if err := l.ai.save(ctx, testJob(t, l, "a.jpg"), syntheticPrediction()); err != nil {
		t.Fatal(err)
	}
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(8 << 20); err != nil {
			t.Error(err)
		}
		f, _, err := r.FormFile("image")
		if err != nil && r.FormValue("text") == "" {
			t.Error(err)
		} else if err == nil {
			if _, err = jpeg.Decode(f); err != nil {
				t.Error(err)
			}
			f.Close()
		}
		_ = json.NewEncoder(w).Encode(syntheticPrediction())
	}))
	defer worker.Close()
	l.ai.ml.config.URL = worker.URL
	l.config.ML.URL = worker.URL
	j := testJob(t, l, "sample.mp4")
	var p Photo
	if err := json.Unmarshal([]byte(j.Metadata), &p); err != nil {
		t.Fatal(err)
	}
	if err := l.ai.indexVideo(ctx, j, p); err != nil {
		t.Fatal(err)
	}
	var tags []faceTag
	if getJSON(t, s, "/api/images/"+j.ID+"/faces", &tags) != 200 || len(tags) != 1 || tags[0].Timestamp == nil {
		t.Fatalf("%+v", tags)
	}
	var photoTags []faceTag
	getJSON(t, s, "/api/images/"+testJob(t, l, "a.jpg").ID+"/faces", &photoTags)
	if tags[0].PersonID != photoTags[0].PersonID {
		t.Fatal("video failed to join existing photo person")
	}
	resp, err := s.Client().Get(s.URL + "/api/people/" + strconv.FormatInt(tags[0].PersonID, 10) + "/image")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatal(resp.Status)
	}
	if _, err = jpeg.Decode(resp.Body); err != nil {
		t.Fatal(err)
	}
	var people struct{ Items []personSummary }
	if getJSON(t, s, "/api/people", &people) != 200 || len(people.Items) != 1 || people.Items[0].Count != 2 || people.Items[0].Videos != 1 {
		t.Fatalf("%+v", people)
	}

	var status struct {
		VideoTotal   int `json:"video_total"`
		VideoIndexed int `json:"video_indexed"`
		VideoPending int `json:"video_pending"`
	}
	if getJSON(t, s, "/api/indexing", &status) != 200 || status.VideoTotal != 1 || status.VideoIndexed != 1 || status.VideoPending != 0 {
		t.Fatalf("%+v", status)
	}
	frameURL := s.URL + "/api/images/" + j.ID + "/frame?timestamp=" + strconv.FormatFloat(*tags[0].Timestamp, 'f', -1, 64)
	response, e := s.Client().Get(frameURL)
	if e != nil {
		t.Fatal(e)
	}
	if response.StatusCode != 200 {
		t.Fatal(response.Status)
	}
	if _, e = jpeg.Decode(response.Body); e != nil {
		t.Fatal(e)
	}
	response.Body.Close()

	var search struct {
		Items []Photo
		Total int
	}
	if getJSON(t, s, "/api/search?q=red&media_type=videos", &search) != 200 || search.Total != 1 || len(search.Items) != 1 || search.Items[0].ID != j.ID {
		t.Fatalf("%+v", search)
	}
	// Reindex keeps one representative and its manual assignment.
	if postTest(t, s, "/api/faces/"+strconv.FormatInt(tags[0].ID, 10), `{"name":"Video person"}`) != 200 {
		t.Fatal("tag")
	}

	for _, tc := range []struct {
		media string
		want  int
	}{{"all", 1}, {"videos", 1}, {"images", 0}} {
		var matches struct{ Items []personSuggestion }
		url := "/api/people/" + strconv.FormatInt(photoTags[0].PersonID, 10) + "/suggestions?min_score=0&media_type=" + tc.media
		if getJSON(t, s, url, &matches) != 200 || len(matches.Items) != tc.want {
			t.Fatalf("%s: %+v", tc.media, matches)
		}
	}
	if err := l.ai.indexVideo(ctx, j, p); err != nil {
		t.Fatal(err)
	}
	getJSON(t, s, "/api/images/"+j.ID+"/faces", &tags)
	if len(tags) != 1 || tags[0].Name != "Video person" {
		t.Fatalf("%+v", tags)
	}
	if postTest(t, s, "/api/curation/"+j.ID, `{"hidden":true}`) != 200 {
		t.Fatal("hide")
	}
	response, e = s.Client().Get(frameURL)
	if e != nil {
		t.Fatal(e)
	}
	response.Body.Close()
	if response.StatusCode != 404 {
		t.Fatal("hidden video frame exposed", response.Status)
	}
	response, e = s.Client().Get(frameURL + "&hidden=only")
	if e != nil {
		t.Fatal(e)
	}
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatal("hidden video review unavailable", response.Status)
	}
	if _, e = l.db.Exec("UPDATE images SET signature='changed' WHERE id=?", j.ID); e != nil {
		t.Fatal(e)
	}
	response, e = s.Client().Get(frameURL + "&hidden=only")
	if e != nil {
		t.Fatal(e)
	}
	response.Body.Close()
	if response.StatusCode != 404 {
		t.Fatal("stale frame exposed", response.Status)
	}

}
