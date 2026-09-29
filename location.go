package main

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/RoyChong5053/TavernLab/internal/obs"
	"github.com/RoyChong5053/TavernLab/internal/settings"
)

// LocationPoint is one raw GPS report from the phone (JSON in = JSON stored).
type LocationPoint struct {
	Lat      float64 `json:"lat"`
	Lon      float64 `json:"lon"`
	Acc      float64 `json:"acc,omitempty"`
	Provider string  `json:"provider,omitempty"`
	Tst      int64   `json:"tst"` // unix seconds (ms auto-converted)
	Batt     int     `json:"batt,omitempty"`
	Received string  `json:"received_at,omitempty"`
}

// LocationGeocode is the stripped Paikka result cached beside the point.
type LocationGeocode struct {
	Lat       float64  `json:"lat"`
	Lon       float64  `json:"lon"`
	Place     string   `json:"place,omitempty"`
	Hierarchy []string `json:"hierarchy,omitempty"`
	Tst       int64    `json:"tst,omitempty"`
	Ms        int64    `json:"ms,omitempty"`
}

var locationMu sync.Mutex

func locationLatestPath(root string) string { return filepath.Join(root, "location_latest.json") }
func locationGeocodePath(root string) string {
	return filepath.Join(root, "location_geocode.json")
}

func normTst(t int64) int64 {
	if t > 1_000_000_000_000 { // ms -> s
		return t / 1000
	}
	return t
}

func validLatLon(lat, lon float64) bool {
	if math.IsNaN(lat) || math.IsNaN(lon) {
		return false
	}
	return lat >= -90 && lat <= 90 && lon >= -180 && lon <= 180
}

// saveLocationPoint appends raw JSONL + refreshes latest (atomic rename).
func saveLocationPoint(root string, p LocationPoint) error {
	locationMu.Lock()
	defer locationMu.Unlock()
	day := time.Now().Format("2006-01-02")
	dir := filepath.Join(root, "locations")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, _ := json.Marshal(p)
	f, err := os.OpenFile(filepath.Join(dir, day+".jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	_, werr := fmt.Fprintln(f, string(b))
	cerr := f.Close()
	if werr != nil {
		return werr
	}
	if cerr != nil {
		return cerr
	}
	tmp := locationLatestPath(root) + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, locationLatestPath(root))
}

func loadLocationLatest(root string) (LocationPoint, bool) {
	var p LocationPoint
	b, err := os.ReadFile(locationLatestPath(root))
	if err != nil {
		return p, false
	}
	// File holds one JSON object (+ trailing newline).
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(b))), &p); err != nil {
		return p, false
	}
	p.Tst = normTst(p.Tst)
	return p, true
}

func loadLocationGeocode(root string) (LocationGeocode, bool) {
	var g LocationGeocode
	b, err := os.ReadFile(locationGeocodePath(root))
	if err != nil {
		return g, false
	}
	if err := json.Unmarshal(b, &g); err != nil {
		return g, false
	}
	return g, true
}

// locationHTTP is the short-timeout client for read-path Paikka refreshes
// (chat assemble hits this on geocode cache miss).
var locationHTTP = &http.Client{Timeout: 8 * time.Second}

// locationPlace picks a readable name: POI display_name first, finest
// hierarchy unit next (Paikka often returns place="" with only hierarchy).
func locationPlace(g LocationGeocode) string {
	if g.Place != "" {
		return g.Place
	}
	if len(g.Hierarchy) > 0 {
		return g.Hierarchy[len(g.Hierarchy)-1]
	}
	return "未知地点"
}
func gridKey(lat, lon float64) string {
	return strconv.FormatFloat(math.Round(lat*1e4)/1e4, 'f', 4, 64) + "," +
		strconv.FormatFloat(math.Round(lon*1e4)/1e4, 'f', 4, 64)
}

// reversePaikka queries self-hosted Paikka and strips bulky fields (boundary,
// geometry_url). Returns place + hierarchy names only.
func reversePaikka(client *http.Client, base string, lat, lon float64) (LocationGeocode, error) {
	g := LocationGeocode{Lat: lat, Lon: lon}
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		return g, fmt.Errorf("paikka disabled")
	}
	u, err := url.Parse(base + "/api/v1/reverse")
	if err != nil {
		return g, err
	}
	q := u.Query()
	q.Set("lat", strconv.FormatFloat(lat, 'f', 6, 64))
	q.Set("lon", strconv.FormatFloat(lon, 'f', 6, 64))
	q.Set("lang", "zh")
	q.Set("limit", "1")
	u.RawQuery = q.Encode()
	req, err := http.NewRequest("GET", u.String(), nil)
	if err != nil {
		return g, err
	}
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return g, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return g, fmt.Errorf("paikka status %d", resp.StatusCode)
	}
	var body struct {
		Results []struct {
			DisplayName string            `json:"display_name"`
			Names       map[string]string `json:"names"`
			Hierarchy   []struct {
				Name string `json:"name"`
			} `json:"hierarchy"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return g, err
	}
	g.Ms = time.Since(start).Milliseconds()
	if len(body.Results) == 0 {
		return g, nil
	}
	r0 := body.Results[0]
	g.Place = r0.DisplayName
	if g.Place == "" && r0.Names != nil {
		g.Place = r0.Names["default"]
	}
	for _, h := range r0.Hierarchy {
		if h.Name != "" {
			g.Hierarchy = append(g.Hierarchy, h.Name)
		}
	}
	return g, nil
}

// ensureGeocode returns cached geocode or refreshes it synchronously (short
// timeout) so /api/logs captures both ingest and reverse lines.
func ensureGeocode(root string, s settings.Settings, client *http.Client, p LocationPoint) LocationGeocode {
	if g, ok := loadLocationGeocode(root); ok && gridKey(g.Lat, g.Lon) == gridKey(p.Lat, p.Lon) && locationPlace(g) != "未知地点" {
		return g
	}
	g, err := reversePaikka(client, s.PaikkaURL, p.Lat, p.Lon)
	g.Tst = time.Now().Unix()
	if err != nil {
		obs.Warn("location geocode failed", map[string]any{"error": err.Error(), "lat": p.Lat, "lon": p.Lon})
		if old, ok := loadLocationGeocode(root); ok {
			return old
		}
		return g
	}
	obs.Info("location geocode", map[string]any{
		"place": g.Place, "hierarchy": strings.Join(g.Hierarchy, ">"), "ms": g.Ms,
	})
	locationMu.Lock()
	b, _ := json.Marshal(g)
	_ = os.WriteFile(locationGeocodePath(root), append(b, '\n'), 0o644)
	locationMu.Unlock()
	return g
}

func formatAge(d time.Duration) string {
	m := int(d.Minutes())
	if m < 1 {
		return "刚刚"
	}
	if m < 60 {
		return fmt.Sprintf("%d分钟前", m)
	}
	h := m / 60
	if h < 24 {
		return fmt.Sprintf("%d小时前", h)
	}
	return fmt.Sprintf("%d天前", h/24)
}

// locationShort renders "PJU 8, Petaling Jaya": place + nearest parent
// with a different name (skips the duplicated tail of the hierarchy).
func locationShort(g LocationGeocode) string {
	place := locationPlace(g)
	parent := ""
	for i := len(g.Hierarchy) - 1; i >= 0; i-- {
		if g.Hierarchy[i] != "" && g.Hierarchy[i] != place {
			parent = g.Hierarchy[i]
			break
		}
	}
	if parent != "" {
		return place + ", " + parent
	}
	return place
}

func haversineKm(lat1, lon1, lat2, lon2 float64) float64 {
	const r = 6371.0
	dLat := (lat2 - lat1) * math.Pi / 180
	dLon := (lon2 - lon1) * math.Pi / 180
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1*math.Pi/180)*math.Cos(lat2*math.Pi/180)*math.Sin(dLon/2)*math.Sin(dLon/2)
	return 2 * r * math.Asin(math.Sqrt(a))
}

// loadRecentPoints reads yesterday + today jsonl (newest last, capped).
func loadRecentPoints(root string, cap int) []LocationPoint {
	var out []LocationPoint
	now := time.Now()
	for _, day := range []string{now.AddDate(0, 0, -1).Format("2006-01-02"), now.Format("2006-01-02")} {
		b, err := os.ReadFile(filepath.Join(root, "locations", day+".jsonl"))
		if err != nil {
			continue
		}
		for _, ln := range strings.Split(string(b), "\n") {
			ln = strings.TrimSpace(ln)
			if ln == "" {
				continue
			}
			var p LocationPoint
			if json.Unmarshal([]byte(ln), &p) != nil {
				continue
			}
			p.Tst = normTst(p.Tst)
			out = append(out, p)
		}
	}
	if len(out) > cap {
		out = out[len(out)-cap:]
	}
	return out
}

// locationDwell infers 移动中 / 静止停留约N分钟 from recent history.
// Returns "" when there is not enough history to say anything.
func locationDwell(root string, p LocationPoint) string {
	pts := loadRecentPoints(root, 200)
	if len(pts) < 2 {
		return ""
	}
	prev := pts[len(pts)-2]
	gap := p.Tst - prev.Tst
	if gap < 0 {
		gap = 0
	}
	if haversineKm(prev.Lat, prev.Lon, p.Lat, p.Lon) > 0.25 && gap < 40*60 {
		return "移动中"
	}
	// Walk back while inside 150m and consecutive gaps stay under 40min.
	first := p.Tst
	for i := len(pts) - 1; i > 0; i-- {
		if haversineKm(pts[i-1].Lat, pts[i-1].Lon, p.Lat, p.Lon) > 0.15 {
			break
		}
		if pts[i].Tst-pts[i-1].Tst > 40*60 {
			break
		}
		first = pts[i-1].Tst
	}
	d := p.Tst - first
	if d < 0 {
		d = 0
	}
	if d < 3*60 {
		return "短暂停留"
	}
	if d < 3600 {
		return fmt.Sprintf("静止停留约%d分钟", int(d/60))
	}
	h, m := int(d/3600), int(d%3600/60)
	if m >= 5 {
		return fmt.Sprintf("静止停留约%d小时%d分", h, m)
	}
	return fmt.Sprintf("静止停留约%d小时", h)
}

// renderLocationText builds the {{location}} macro value for time_anchor.
// Bare facts only (no behaviour instructions — those belong in the system
// block): "PJU 8, Petaling Jaya · 静止停留约12分钟".
func renderLocationText(root string, s settings.Settings, client *http.Client, now time.Time) string {
	if !s.LocationEnabled {
		return ""
	}
	p, ok := loadLocationLatest(root)
	if !ok {
		return ""
	}
	staleMin := s.LocationStaleMin
	if staleMin <= 0 {
		staleMin = 30
	}
	age := now.Unix() - p.Tst
	if age < 0 {
		age = 0
	}
	stale := age > int64(staleMin*60)
	g, _ := loadLocationGeocode(root)
	if gridKey(g.Lat, g.Lon) != gridKey(p.Lat, p.Lon) || locationPlace(g) == "未知地点" {
		// Lazy refresh on read path (chat/assemble); ingest path already
		// refreshed via ensureGeocode, so this is usually a cache hit.
		g = ensureGeocode(root, s, client, p)
	}
	short := locationShort(g)
	coarse := ""
	if p.Acc > 500 {
		coarse = "，粗精度"
	}
	if stale {
		return fmt.Sprintf("%s · %s更新（已过期）%s", short, formatAge(time.Duration(age)*time.Second), coarse)
	}
	if dwell := locationDwell(root, p); dwell != "" {
		return fmt.Sprintf("%s · %s%s", short, dwell, coarse)
	}
	return fmt.Sprintf("%s · %s更新%s", short, formatAge(time.Duration(age)*time.Second), coarse)
}
