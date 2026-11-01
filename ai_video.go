package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"image/jpeg"
	"math"
	"net/http"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

var errVideoWorker = errors.New("video inference unavailable")
var errNoVideoFrame = errors.New("no frame at timestamp")

const videoSchema = `CREATE TABLE IF NOT EXISTS video_frames(
 image_id TEXT NOT NULL, signature TEXT NOT NULL, models TEXT NOT NULL,
 timestamp REAL NOT NULL, preview BLOB NOT NULL, clip BLOB NOT NULL,
 PRIMARY KEY(image_id,timestamp));
 CREATE INDEX IF NOT EXISTS video_frame_status ON video_frames(image_id,signature,models);`

func faceBoxJSON(f mlFace) []byte {
	if f.Timestamp == nil {
		b, _ := json.Marshal(f.Box)
		return b
	}
	b, _ := json.Marshal(struct {
		faceBox
		Timestamp  float64   `json:"timestamp"`
		Timestamps []float64 `json:"timestamps,omitempty"`
	}{f.Box, *f.Timestamp, f.Timestamps})
	return b
}

// Bound work on long recordings while covering the entire duration.
func videoSampleTimes(duration float64) []float64 {
	if duration <= 0 || math.IsNaN(duration) || math.IsInf(duration, 0) {
		return nil
	}
	n := max(3, int(math.Ceil(duration/10)))
	n = min(n, 240)
	times := make([]float64, n)
	for i := range times {
		times[i] = duration * (float64(i) + .1) / float64(n)
	}
	times[0] = 0
	return times
}

func (l *Library) videoFrame(ctx context.Context, p Photo, seconds float64) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, l.config.FFmpeg, "-nostdin", "-v", "error", "-threads", "1", "-ss", strconv.FormatFloat(seconds, 'f', 3, 64), "-i", p.AbsolutePath, "-map", "0:v:0", "-frames:v", "1", "-vf", "scale=1280:1280:force_original_aspect_ratio=decrease", "-q:v", "3", "-f", "image2pipe", "-vcodec", "mjpeg", "pipe:1")
	configurePreview(cmd)
	var out, stderr limitedBuffer
	out.max = 8 << 20
	stderr.max = 4096
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("video frame: %w: %s", err, stderr.String())
	}
	if out.Len() == 0 {
		return nil, fmt.Errorf("%w: %.3f", errNoVideoFrame, seconds)
	}
	return out.Bytes(), nil
}

func (a *AI) indexNextVideo(ctx context.Context) error {
	a.videoWorked = false
	var j aiJob
	// Read pool: the scan must not hold the write connection (see jobs).
	err := a.read.QueryRowContext(ctx, `SELECT i.id,i.signature,metadata_json(i.metadata) FROM catalog.images i INDEXED BY image_video_status LEFT JOIN ai_images a ON a.image_id=i.id WHERE NOT (`+photoOnlySQL+`) AND (a.image_id IS NULL OR a.signature!=i.signature OR a.models!=? OR (a.error!='' AND a.retry_at<=?)) ORDER BY i.id LIMIT 1`, a.models, time.Now().Unix()).Scan(&j.ID, &j.Signature, &j.Metadata)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	a.videoWorked = true
	var p Photo
	if err = json.Unmarshal([]byte(j.Metadata), &p); err != nil {
		return err
	}
	a.mu.Lock()
	a.active = p.Path
	a.mu.Unlock()
	defer func() { a.mu.Lock(); a.active = ""; a.mu.Unlock() }()
	err = a.indexVideo(ctx, j, p)
	if err != nil && ctx.Err() == nil && !errors.Is(err, errVideoWorker) {
		a.recordFailure(ctx, j, err)
	}
	return err
}

func (a *AI) indexVideo(ctx context.Context, j aiJob, p Photo) error {
	probe := filepath.Join(filepath.Dir(a.lib.config.FFmpeg), "ffprobe")
	if filepath.Ext(a.lib.config.FFmpeg) == ".exe" {
		probe += ".exe"
	}
	probeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	cmd := exec.CommandContext(probeCtx, probe, "-v", "error", "-show_entries", "stream=duration:format=duration", "-select_streams", "v:0", "-of", "json", p.AbsolutePath)
	configurePreview(cmd)
	output, err := cmd.Output()
	cancel()
	if err != nil {
		return fmt.Errorf("video duration (FFprobe required): %w", err)
	}
	var metadata struct {
		Streams []struct {
			Duration string `json:"duration"`
		} `json:"streams"`
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
	}
	if err = json.Unmarshal(output, &metadata); err != nil {
		return fmt.Errorf("video duration: %w", err)
	}
	duration, _ := strconv.ParseFloat(metadata.Format.Duration, 64)
	if len(metadata.Streams) > 0 {
		if streamDuration, e := strconv.ParseFloat(metadata.Streams[0].Duration, 64); e == nil && streamDuration > 0 {
			duration = streamDuration
		}
	}
	if len(videoSampleTimes(duration)) == 0 {
		return fmt.Errorf("invalid video duration")
	}
	type frame struct {
		at            float64
		preview, clip []byte
	}
	var frames []frame
	var faces []mlFace
	var vectors []vector
	var sum vector
	var lastPreview []byte
	for _, at := range a.lib.videoSamplingPlan(ctx, p, duration) {
		if !a.waitIndexing(ctx, true) {
			return ctx.Err()
		}
		preview, e := a.lib.videoFrame(ctx, p, at)
		if errors.Is(e, errNoVideoFrame) {
			continue
		}
		if e != nil {
			return e
		}
		if bytes.Equal(lastPreview, preview) {
			continue
		}
		lastPreview = preview
		a.pipeline.inFlight.Add(1)
		prediction, e := a.ml.predict(ctx, preview, "")
		a.pipeline.inFlight.Add(-1)
		if e != nil {
			a.mu.Lock()
			a.online = false
			a.mu.Unlock()
			return fmt.Errorf("%w: %v", errVideoWorker, e)
		}
		a.mu.Lock()
		a.online = true
		a.mu.Unlock()
		v, e := parseVector(prediction.Clip)
		if e != nil {
			return e
		}
		if sum == nil {
			sum = make(vector, len(v))
		}
		if len(sum) != len(v) {
			return fmt.Errorf("video embedding dimension changed")
		}
		for i := range v {
			sum[i] += v[i]
		}
		frames = append(frames, frame{at, preview, v.blob()})
		for _, f := range prediction.Faces {
			if f.Score < .8 || prediction.Width < 1 || prediction.Height < 1 {
				continue
			}
			v, e := parseVector(f.Embedding)
			if e != nil {
				return e
			}
			duplicate := -1
			for i, other := range vectors {
				if *faces[i].Timestamp != at && similarity(v, other) > max(.7, a.lib.config.ML.FaceSimilarity) {
					duplicate = i
					break
				}
			}
			// Keep the highest-confidence representative of each appearance group.
			if duplicate >= 0 && faces[duplicate].Score >= f.Score {
				faces[duplicate].Timestamps = append(faces[duplicate].Timestamps, at)
				continue
			}
			f.Box.X1 = max(0, min(1280, f.Box.X1*1280/float64(prediction.Width)))
			f.Box.X2 = max(0, min(1280, f.Box.X2*1280/float64(prediction.Width)))
			f.Box.Y1 = max(0, min(1280, f.Box.Y1*1280/float64(prediction.Height)))
			f.Box.Y2 = max(0, min(1280, f.Box.Y2*1280/float64(prediction.Height)))
			timestamp := at
			f.Timestamp = &timestamp
			f.Timestamps = []float64{at}
			if duplicate >= 0 {
				f.Timestamps = append(faces[duplicate].Timestamps, at)
				faces[duplicate] = f
				vectors[duplicate] = v
			} else {
				if len(faces) >= 1000 {
					return fmt.Errorf("too many video face groups")
				}
				faces = append(faces, f)
				vectors = append(vectors, v)
			}
		}
	}
	if len(frames) == 0 {
		return fmt.Errorf("video contains no decodable frames")
	}
	raw, _ := json.Marshal(sum)
	// Retained frames are committed before face rows, so portraits are always available.
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "DELETE FROM video_frames WHERE image_id=?", j.ID); err != nil {
		return err
	}
	for _, f := range frames {
		if _, err = tx.ExecContext(ctx, "INSERT INTO video_frames VALUES(?,?,?,?,?,?)", j.ID, j.Signature, a.models, f.at, f.preview, f.clip); err != nil {
			return err
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	if err = a.save(ctx, j, mlPrediction{Clip: raw, Faces: faces, Width: 1280, Height: 1280}); err != nil {
		return err
	}
	a.statusRevision.Add(1)
	a.selectionRevision.Add(1)
	a.pipeline.completed.Add(1)
	a.mu.Lock()
	a.lastError = ""
	a.lastSuccess = time.Now().UTC().Format(time.RFC3339)
	a.mu.Unlock()
	return nil
}

func (a *AI) videoFrameAPI(w http.ResponseWriter, r *http.Request) error {
	at, err := strconv.ParseFloat(r.URL.Query().Get("timestamp"), 64)
	if err != nil || math.IsNaN(at) || math.IsInf(at, 0) || at < 0 {
		return bad(400, "Invalid timestamp")
	}
	// Apply the same visibility rules as face tags.
	tags, err := a.faceTags(r, []string{r.PathValue("id")})
	if err != nil {
		return err
	}
	found := false
	for _, f := range tags[r.PathValue("id")] {
		if f.Timestamp != nil && *f.Timestamp == at {
			found = true
			break
		}
	}
	if !found {
		return bad(404, "Frame not found")
	}
	var data []byte
	err = a.read.QueryRowContext(r.Context(), `SELECT v.preview FROM video_frames v JOIN catalog.images i ON i.id=v.image_id AND i.signature=v.signature WHERE v.image_id=? AND v.timestamp=? AND v.models=?`, r.PathValue("id"), at, a.models).Scan(&data)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "image/jpeg")
	_, err = w.Write(data)
	return err
}

// Decode the retained frame at its own dimensions, then apply the normalized box.
func (l *Library) videoFaceThumbnail(ctx context.Context, p Photo, box string) ([]byte, error) {
	var b struct {
		faceBox
		Timestamp float64 `json:"timestamp"`
	}
	if err := json.Unmarshal([]byte(box), &b); err != nil {
		return nil, err
	}
	var preview []byte
	if err := l.ai.read.QueryRowContext(ctx, `SELECT v.preview FROM video_frames v JOIN catalog.images i ON i.id=v.image_id AND i.signature=v.signature WHERE v.image_id=? AND v.timestamp=? AND v.models=?`, p.ID, b.Timestamp, l.ai.models).Scan(&preview); err != nil {
		return nil, err
	}
	img, err := jpeg.Decode(bytes.NewReader(preview))
	if err != nil {
		return nil, err
	}
	cropped, err := cropFace(img, &faceCrop{Box: b.faceBox, Width: 1280, Height: 1280})
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	err = jpeg.Encode(&out, cropped, &jpeg.Options{Quality: 80})
	return out.Bytes(), err
}

func reviewMediaSQL(r *http.Request, path string) (string, error) {
	photo := strings.ReplaceAll(photoOnlySQL, "i.path", path)
	switch r.URL.Query().Get("media_type") {
	case "", "all":
		return "1=1", nil
	case "images":
		return photo, nil
	case "videos":
		return "NOT (" + photo + ")", nil
	default:
		return "", bad(422, "Invalid media_type")
	}
}

// Keep photo_count as the legacy total-asset count; video_count separates media.
const personMediaCountsSQL = ",count(DISTINCT CASE WHEN f.image_id IN(SELECT id FROM catalog.images i INDEXED BY image_video_status WHERE NOT (" + photoOnlySQL + ")) THEN f.image_id END)"

// Scene sampling is an optional bounded pass; regular samples always span the video.
func (l *Library) videoSamplingPlan(ctx context.Context, p Photo, duration float64) []float64 {
	times := videoSampleTimes(duration)
	scan, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(scan, l.config.FFmpeg, "-nostdin", "-v", "error", "-threads", "1", "-i", p.AbsolutePath, "-an", "-vf", "scale=160:-2,select=gt(scene\\,0.35),metadata=print:file=-", "-frames:v", "120", "-f", "null", "-")
	configurePreview(cmd)
	var out limitedBuffer
	out.max = 128 << 10
	cmd.Stdout = &out
	if cmd.Run() == nil {
		re := regexp.MustCompile(`pts_time:([0-9.]+)`)
		for _, match := range re.FindAllStringSubmatch(out.String(), 120) {
			at, e := strconv.ParseFloat(match[1], 64)
			if e == nil && at >= 0 && at < duration {
				times = append(times, at)
			}
		}
	}
	sort.Float64s(times)
	unique := times[:0]
	for _, at := range times {
		if len(unique) == 0 || at-unique[len(unique)-1] >= .25 {
			unique = append(unique, at)
		}
	}
	return unique
}
