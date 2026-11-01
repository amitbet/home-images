package main

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// videoProbeVersion is stored in a video's metadata once FFprobe has been
// asked about it. Scans re-read indexed videos with an older (or no) version,
// so raising it backfills dates and dimensions without touching photos.
const videoProbeVersion = 1

const videoProbeKey = "video_probe"

// videoNeedsProbeSQL selects catalog rows whose video metadata predates the
// current probe. The path test runs first so photos are never decompressed.
var videoNeedsProbeSQL = `(lower(substr(path,-4)) IN ('.mp4','.m4v','.mov','.ogv') OR lower(substr(path,-5))='.webm') AND coalesce(json_extract(metadata_json(metadata),'$.exif.` + videoProbeKey + `'),0)<` + strconv.Itoa(videoProbeVersion)

type videoProbe struct {
	Streams []struct {
		CodecType    string            `json:"codec_type"`
		CodecName    string            `json:"codec_name"`
		Width        int               `json:"width"`
		Height       int               `json:"height"`
		Tags         map[string]string `json:"tags"`
		SideDataList []struct {
			Rotation float64 `json:"rotation"`
		} `json:"side_data_list"`
	} `json:"streams"`
	Format struct {
		FormatName string            `json:"format_name"`
		Duration   string            `json:"duration"`
		Tags       map[string]string `json:"tags"`
	} `json:"format"`
}

// probeVideo fills in dimensions, duration and the capture date. Failure is
// not fatal: the video stays browsable with its file dates.
func (l *Library) probeVideo(ctx context.Context, p *Photo) {
	p.Exif[videoProbeKey] = videoProbeVersion
	ffmpeg, err := exec.LookPath(l.config.FFmpeg)
	if err != nil {
		return
	}
	probe := filepath.Join(filepath.Dir(ffmpeg), "ffprobe"+filepath.Ext(ffmpeg))
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, probe, "-v", "error", "-show_entries",
		"format=format_name,duration:format_tags:stream=codec_type,codec_name,width,height:stream_tags:stream_side_data=rotation",
		"-of", "json", p.AbsolutePath)
	configurePreview(cmd)
	var out limitedBuffer
	out.max = 1 << 20
	cmd.Stdout = &out
	if cmd.Run() != nil {
		return
	}
	var data videoProbe
	if json.Unmarshal(out.Bytes(), &data) != nil {
		return
	}
	applyVideoProbe(p, data)
}

func applyVideoProbe(p *Photo, data videoProbe) {
	info := map[string]any{}
	if data.Format.FormatName != "" {
		info["container"] = data.Format.FormatName
	}
	if d, err := strconv.ParseFloat(data.Format.Duration, 64); err == nil && d > 0 {
		info["duration"] = d
	}
	var dates []string
	for _, key := range []string{"com.apple.quicktime.creationdate", "creation_time", "date"} {
		if v := data.Format.Tags[key]; v != "" {
			dates = append(dates, v)
		}
	}
	for _, s := range data.Streams {
		if s.CodecType == "video" && p.Width == 0 && s.Width > 0 && s.Height > 0 {
			p.Width, p.Height = s.Width, s.Height
			info["codec"] = s.CodecName
			rotation := 0.0
			if r, err := strconv.ParseFloat(s.Tags["rotate"], 64); err == nil {
				rotation = r
			}
			for _, side := range s.SideDataList {
				if side.Rotation != 0 {
					rotation = side.Rotation
				}
			}
			if r := int(rotation) % 180; r == 90 || r == -90 {
				p.Width, p.Height = p.Height, p.Width
			}
		}
		if v := s.Tags["creation_time"]; v != "" {
			dates = append(dates, v)
		}
	}
	// Phones write true UTC to creation_time. Samsung records the local
	// offset next to it; other Android phones get the server's time zone.
	zone := (*time.Location)(nil)
	if o := data.Format.Tags["com.samsung.android.utc_offset"]; o != "" {
		if t, err := time.Parse("-0700", o); err == nil {
			zone = t.Location()
		}
	} else if data.Format.Tags["com.android.version"] != "" {
		zone = time.Local
	}
	for _, raw := range dates {
		if taken, offset, ok := videoCaptureTime(raw, zone); ok {
			p.TakenAt = &taken
			if offset != "" {
				p.TakenAtOffset = &offset
			}
			info["creation_time"] = raw
			break
		}
	}
	if len(info) > 0 {
		p.Exif["Video"] = info
	}
}

// videoCaptureTime turns a container date into the EXIF-style wall-clock
// "YYYY:MM:DD HH:MM:SS" used for photos. Apple's creationdate carries the
// local offset. QuickTime creation_time is nominally UTC, but older cameras
// (e.g. Minolta) wrote their local clock there, so without a known phone
// zone it is used as-is.
func videoCaptureTime(raw string, zone *time.Location) (taken, offset string, ok bool) {
	raw = strings.TrimSpace(raw)
	for _, layout := range []string{"2006-01-02T15:04:05Z0700", "2006-01-02T15:04:05Z07:00", "2006-01-02T15:04:05.999999999Z07:00", "2006-01-02 15:04:05", "2006-01-02T15:04:05", "2006:01:02 15:04:05"} {
		t, err := time.Parse(layout, raw)
		if err != nil {
			continue
		}
		// Unset camera clocks produce the QuickTime/Unix epochs.
		if t.Year() < 1980 || t.After(time.Now().Add(48*time.Hour)) {
			return "", "", false
		}
		if strings.Contains(layout, "Z07") && !strings.HasSuffix(raw, "Z") {
			offset = t.Format("-07:00")
		} else if zone != nil && strings.HasSuffix(raw, "Z") {
			t = t.In(zone)
			offset = t.Format("-07:00")
		}
		return t.Format("2006:01:02 15:04:05"), offset, true
	}
	return "", "", false
}
