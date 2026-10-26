package main

// GeoNames cities500, admin1 and country data (CC BY 4.0): https://www.geonames.org/.
// Coordinates stay on this server. Only public dataset files are downloaded.
import (
	"archive/zip"
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type geoCity struct {
	point [3]float64
	label string
}
type geoNode struct {
	city        geoCity
	left, right *geoNode
	axis        int
}

func geoPoint(lat, lon float64) [3]float64 {
	lat *= math.Pi / 180
	lon *= math.Pi / 180
	return [3]float64{math.Cos(lat) * math.Cos(lon), math.Cos(lat) * math.Sin(lon), math.Sin(lat)}
}
func geoTree(c []geoCity, depth int) *geoNode {
	if len(c) == 0 {
		return nil
	}
	axis := depth % 3
	sort.Slice(c, func(i, j int) bool { return c[i].point[axis] < c[j].point[axis] })
	mid := len(c) / 2
	return &geoNode{c[mid], geoTree(c[:mid], depth+1), geoTree(c[mid+1:], depth+1), axis}
}
func (n *geoNode) nearest(p [3]float64, best *geoCity, distance *float64) {
	if n == nil {
		return
	}
	d := 0.0
	for i := range p {
		v := p[i] - n.city.point[i]
		d += v * v
	}
	if d < *distance {
		*distance = d
		*best = n.city
	}
	delta := p[n.axis] - n.city.point[n.axis]
	near, far := n.left, n.right
	if delta > 0 {
		near, far = far, near
	}
	near.nearest(p, best, distance)
	if delta*delta < *distance {
		far.nearest(p, best, distance)
	}
}
func (n *geoNode) lookup(lat, lon float64) string {
	if math.IsNaN(lat) || math.IsNaN(lon) || math.IsInf(lat, 0) || math.IsInf(lon, 0) || math.Abs(lat) > 90 || math.Abs(lon) > 180 {
		return ""
	}
	best := geoCity{}
	distance := math.Pow(2*math.Sin(50.0/6371/2), 2) // 50 km maximum; nearest settlement, not an address.
	n.nearest(geoPoint(lat, lon), &best, &distance)
	return best.label
}
func geoLines(r io.Reader, visit func([]string)) error {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 4096), 2<<20)
	for s.Scan() {
		if line := s.Text(); line != "" && !strings.HasPrefix(line, "#") {
			visit(strings.Split(line, "\t"))
		}
	}
	return s.Err()
}
func loadGeo(dir string) (*geoNode, error) {
	countries, admins := map[string]string{}, map[string]string{}
	for _, file := range []string{"countryInfo.txt", "admin1CodesASCII.txt"} {
		f, e := os.Open(filepath.Join(dir, file))
		if e != nil {
			return nil, e
		}
		e = geoLines(f, func(v []string) {
			if file == "countryInfo.txt" && len(v) > 4 {
				countries[v[0]] = v[4]
			} else if file == "admin1CodesASCII.txt" && len(v) > 1 {
				admins[v[0]] = v[1]
			}
		})
		f.Close()
		if e != nil {
			return nil, e
		}
	}
	z, e := zip.OpenReader(filepath.Join(dir, "cities500.zip"))
	if e != nil {
		return nil, e
	}
	defer z.Close()
	var cities []geoCity
	for _, entry := range z.File {
		if entry.Name != "cities500.txt" {
			continue
		}
		r, e := entry.Open()
		if e != nil {
			return nil, e
		}
		e = geoLines(io.LimitReader(r, 256<<20), func(v []string) {
			if len(v) < 19 || v[6] != "P" {
				return
			}
			lat, e1 := strconv.ParseFloat(v[4], 64)
			lon, e2 := strconv.ParseFloat(v[5], 64)
			if e1 != nil || e2 != nil || math.IsNaN(lat) || math.IsNaN(lon) || math.Abs(lat) > 90 || math.Abs(lon) > 180 {
				return
			}
			parts := []string{v[1]}
			for _, s := range []string{admins[v[8]+"."+v[10]], countries[v[8]]} {
				if s != "" && s != parts[len(parts)-1] {
					parts = append(parts, s)
				}
			}
			cities = append(cities, geoCity{geoPoint(lat, lon), strings.Join(parts, ", ")})
		})
		r.Close()
		if e != nil {
			return nil, e
		}
	}
	if len(cities) == 0 {
		return nil, fmt.Errorf("GeoNames dataset has no cities")
	}
	return geoTree(cities, 0), nil
}
func downloadGeo(ctx context.Context, dir string) error {
	if e := os.MkdirAll(dir, 0750); e != nil {
		return e
	}
	for _, name := range []string{"countryInfo.txt", "admin1CodesASCII.txt", "cities500.zip"} {
		if st, e := os.Stat(filepath.Join(dir, name)); e == nil && st.Size() > 0 {
			continue
		}
		request, e := http.NewRequestWithContext(ctx, "GET", "https://download.geonames.org/export/dump/"+name, nil)
		if e != nil {
			return e
		}
		response, e := (&http.Client{Timeout: 3 * time.Minute}).Do(request)
		if e != nil {
			return e
		}
		if response.StatusCode != 200 {
			response.Body.Close()
			return fmt.Errorf("GeoNames %s: HTTP %d", name, response.StatusCode)
		}
		tmp, e := os.CreateTemp(dir, "download-*")
		if e != nil {
			response.Body.Close()
			return e
		}
		n, e := io.Copy(tmp, io.LimitReader(response.Body, (64<<20)+1))
		response.Body.Close()
		closeErr := tmp.Close()
		if e == nil {
			e = closeErr
		}
		if n > 64<<20 {
			e = fmt.Errorf("GeoNames download too large")
		}
		if e == nil {
			e = os.Rename(tmp.Name(), filepath.Join(dir, name))
		}
		os.Remove(tmp.Name())
		if e != nil {
			return e
		}
	}
	return nil
}

// Normalized imagemeta GPS includes an explicit hemisphere. Unset zero values
// have Ref=0 and must never turn photos without GPS into Gulf of Guinea locations.
func photoGPS(p Photo) (float64, float64, bool) {
	gps, _ := p.Exif["GPS"].(map[string]any)
	coordinate := func(key string, positive, negative float64) (float64, bool) {
		v, ok := gps[key].(map[string]any)
		if !ok {
			return 0, false
		}
		value, ok := v["Value"].(float64)
		ref, _ := v["Ref"].(float64)
		if !ok || value < 0 || math.IsNaN(value) || math.IsInf(value, 0) {
			return 0, false
		}
		if ref == negative {
			return -value, true
		}
		return value, ref == positive
	}
	lat, a := coordinate("Latitude", 1, 2)
	lon, b := coordinate("Longitude", 3, 4)
	if a && b {
		return lat, lon, math.Abs(lat) <= 90 && math.Abs(lon) <= 180
	}
	// Legacy Python/goexif exports GPS as DMS arrays plus hemisphere strings.
	dms := func(key, refKey string, limit float64) (float64, bool) {
		raw := p.Exif[key]
		if raw == nil {
			raw = gps[key]
		}
		v, ok := raw.([]any)
		if !ok || len(v) != 3 {
			return 0, false
		}
		total := 0.0
		for i, item := range v {
			var n float64
			switch x := item.(type) {
			case float64:
				n = x
			case string:
				parts := strings.Split(x, "/")
				var e error
				n, e = strconv.ParseFloat(parts[0], 64)
				if e != nil {
					return 0, false
				}
				if len(parts) == 2 {
					den, e := strconv.ParseFloat(parts[1], 64)
					if e != nil || den == 0 {
						return 0, false
					}
					n /= den
				}
			default:
				return 0, false
			}
			if n < 0 || math.IsNaN(n) || math.IsInf(n, 0) || (i > 0 && n >= 60) {
				return 0, false
			}
			total += n / math.Pow(60, float64(i))
		}
		ref, _ := p.Exif[refKey].(string)
		if ref == "" {
			ref, _ = gps[refKey].(string)
		}
		ref = strings.Trim(ref, "\x00 ")
		if key == "GPSLatitude" && ref != "N" && ref != "S" {
			return 0, false
		}
		if key == "GPSLongitude" && ref != "E" && ref != "W" {
			return 0, false
		}
		if ref == "S" || ref == "W" {
			total = -total
		}
		return total, math.Abs(total) <= limit
	}
	lat, a = dms("GPSLatitude", "GPSLatitudeRef", 90)
	lon, b = dms("GPSLongitude", "GPSLongitudeRef", 180)
	return lat, lon, a && b
}
func (l *Library) geocodeBatch(ctx context.Context, tree *geoNode) (int, error) {
	rows, e := l.db.QueryContext(ctx, `SELECT i.id,i.signature,i.metadata FROM images i LEFT JOIN locations g ON g.image_id=i.id WHERE g.image_id IS NULL OR g.signature!=i.signature LIMIT 128`)
	if e != nil {
		return 0, e
	}
	var jobs []aiJob
	for rows.Next() {
		var j aiJob
		if e = rows.Scan(&j.ID, &j.Signature, &j.Metadata); e != nil {
			break
		}
		jobs = append(jobs, j)
	}
	if e == nil {
		e = rows.Err()
	}
	rows.Close()
	if e != nil {
		return 0, e
	}
	for _, j := range jobs {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		var p Photo
		if e = json.Unmarshal([]byte(j.Metadata), &p); e != nil {
			return 0, e
		}
		label := ""
		if lat, lon, ok := photoGPS(p); ok {
			label = tree.lookup(lat, lon)
		}
		_, e = l.db.ExecContext(ctx, `INSERT INTO locations(image_id,signature,label) SELECT id,signature,? FROM images WHERE id=? AND signature=? ON CONFLICT(image_id) DO UPDATE SET signature=excluded.signature,label=excluded.label`, label, j.ID, j.Signature)
		if e != nil {
			return 0, e
		}
	}
	return len(jobs), nil
}
func (l *Library) geocode(ctx context.Context) {
	dir := filepath.Join(l.config.DataDir, "geonames")
	for ctx.Err() == nil {
		if e := downloadGeo(ctx, dir); e != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("Geocoding download: %v", e)
			if !waitContext(ctx, 10*time.Minute) {
				return
			}
			continue
		}
		tree, e := loadGeo(dir)
		if e != nil {
			log.Printf("Geocoding dataset: %v", e)
			return
		}
		log.Printf("Geocoding ready (GeoNames, CC BY 4.0)")
		for ctx.Err() == nil {
			n, e := l.geocodeBatch(ctx, tree)
			if e != nil && ctx.Err() == nil {
				log.Printf("Geocoding: %v", e)
			}
			delay := 100 * time.Millisecond
			if n == 0 || e != nil {
				delay = 30 * time.Second
			}
			if !waitContext(ctx, delay) {
				return
			}
		}
	}
}
