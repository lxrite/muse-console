package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"math"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	windowSec    = 72 * 3600        // chart window
	retentionSec = windowSec + 3600 // journal retention
	gapSec       = 90               // heartbeat gap longer than this => offline
)

// Record is one heartbeat: unix timestamp plus the machine identity
// (boot_id and uptime), used to tell whether an offline period involved
// a machine reboot or just a heartbeat interruption.
type Record struct {
	TS   int64    `json:"ts"`
	Boot string   `json:"boot"`
	Up   *float64 `json:"up"`
}

// Timeline is the in-memory heartbeat array, journaled to disk so history
// survives process restarts.
type Timeline struct {
	mu      sync.Mutex
	records []Record
	file    string
}

func NewTimeline(file string) *Timeline { return &Timeline{file: file} }

func readBootID() string {
	b, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func readUptime() *float64 {
	b, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return nil
	}
	f, err := strconv.ParseFloat(strings.Fields(string(b))[0], 64)
	if err != nil {
		return nil
	}
	return &f
}

func makeRecord() Record {
	return Record{TS: time.Now().Unix(), Boot: readBootID(), Up: readUptime()}
}

// Load replays the disk journal into memory. It understands the current
// JSON lines, "RUN <ts>" markers, and legacy plain-integer lines.
func (t *Timeline) Load() {
	f, err := os.Open(t.file)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("timeline: open journal: %v", err)
		}
		return
	}
	defer f.Close()

	var recs []Record
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "RUN ") {
			continue // legacy restart markers: no longer written, ignored
		}
		if strings.HasPrefix(line, "{") {
			var r Record
			if err := json.Unmarshal([]byte(line), &r); err == nil && r.TS > 0 {
				recs = append(recs, r)
			}
			continue
		}
		if ts, err := strconv.ParseInt(line, 10, 64); err == nil {
			recs = append(recs, Record{TS: ts})
		}
	}

	cutoff := time.Now().Unix() - retentionSec
	byTS := make(map[int64]Record)
	for _, r := range recs {
		if r.TS >= cutoff {
			byTS[r.TS] = r // dedupe, keep last
		}
	}
	recs = recs[:0]
	for _, r := range byTS {
		recs = append(recs, r)
	}
	sort.Slice(recs, func(i, j int) bool { return recs[i].TS < recs[j].TS })

	t.mu.Lock()
	t.records = recs
	t.mu.Unlock()
	t.rewrite() // trim + normalize the journal
	log.Printf("timeline: loaded %d records", len(recs))
}

func (t *Timeline) journal(line string) {
	f, err := os.OpenFile(t.file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		log.Printf("timeline: journal append: %v", err)
		return
	}
	defer f.Close()
	fmt.Fprintln(f, line)
}

func (t *Timeline) rewrite() {
	t.mu.Lock()
	defer t.mu.Unlock()
	f, err := os.Create(t.file)
	if err != nil {
		log.Printf("timeline: rewrite: %v", err)
		return
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	for _, r := range t.records {
		b, _ := json.Marshal(r)
		w.Write(b)
		w.WriteByte('\n')
	}
	w.Flush()
}

// Run records a heartbeat every minute, aligned to minute boundaries.
// Call it in a goroutine.
func (t *Timeline) Run() {
	rec := makeRecord()
	t.mu.Lock()
	t.records = append(t.records, rec)
	t.mu.Unlock()
	if b, err := json.Marshal(rec); err == nil {
		t.journal(string(b))
	}

	for {
		now := time.Now()
		next := now.Truncate(time.Minute).Add(time.Minute)
		time.Sleep(time.Until(next))

		rec := makeRecord()
		t.mu.Lock()
		if len(t.records) > 0 && rec.TS <= t.records[len(t.records)-1].TS {
			t.mu.Unlock()
			continue // clock jumped backward; skip
		}
		t.records = append(t.records, rec)
		cutoff := rec.TS - retentionSec
		for len(t.records) > 0 && t.records[0].TS < cutoff {
			t.records = t.records[1:]
		}
		t.mu.Unlock()
		if b, err := json.Marshal(rec); err == nil {
			t.journal(string(b))
		}
	}
}

// ---- gap classification ----

type Segment struct {
	Start, End int64
	State      string // active | offline | unknown
}

// stateName returns the localized display name for a timeline state.
func stateName(lang, state string) string {
	switch state {
	case "active":
		return T(lang, "state.active")
	case "offline":
		return T(lang, "state.offline")
	default:
		return T(lang, "state.unknown")
	}
}

var stateColors = map[string]string{
	"active":  "#22c55e",
	"offline": "#ef4444",
	"unknown": "#d1d5db",
}

// rebooted reports whether the machine was powered off (and booted again)
// between two consecutive records: the boot_id changed, or uptime went
// backwards.
func rebooted(a, b Record) bool {
	if a.Boot != "" && b.Boot != "" && a.Boot != b.Boot {
		return true
	}
	if a.Up != nil && b.Up != nil && *b.Up < *a.Up-5 {
		return true
	}
	return false
}

// OfflineEvent is one offline period. Rebooted tells whether the machine
// was powered off and booted again during the gap (boot_id changed),
// as opposed to a mere heartbeat interruption.
type OfflineEvent struct {
	Start, End int64
	Rebooted   bool
}

func (t *Timeline) buildSegments(now int64) (segs []Segment, offlines []OfflineEvent) {
	start := now - windowSec
	t.mu.Lock()
	var recs []Record
	for _, r := range t.records {
		if r.TS >= start {
			recs = append(recs, r)
		}
	}
	t.mu.Unlock()

	if len(recs) == 0 {
		return []Segment{{start, now, "unknown"}}, nil
	}
	if recs[0].TS > start {
		segs = append(segs, Segment{start, recs[0].TS, "unknown"})
	}
	for i := 0; i+1 < len(recs); i++ {
		a, b := recs[i], recs[i+1]
		if b.TS-a.TS <= gapSec {
			segs = append(segs, Segment{a.TS, b.TS, "active"})
			continue
		}
		// No heartbeat for longer than gapSec: the service was offline.
		// Rebooted records whether the machine itself went down.
		rb := rebooted(a, b)
		segs = append(segs, Segment{a.TS, b.TS, "offline"})
		offlines = append(offlines, OfflineEvent{a.TS, b.TS, rb})
	}
	if now-recs[len(recs)-1].TS <= gapSec {
		segs = append(segs, Segment{recs[len(recs)-1].TS, now, "active"})
	} else {
		// Heartbeats stopped and never resumed: currently offline.
		last := recs[len(recs)-1].TS
		segs = append(segs, Segment{last, now, "offline"})
		offlines = append(offlines, OfflineEvent{last, now, false})
	}

	// merge adjacent segments of the same state
	merged := segs[:0]
	for _, s := range segs {
		if n := len(merged); n > 0 && merged[n-1].State == s.State && s.Start <= merged[n-1].End+1 {
			if s.End > merged[n-1].End {
				merged[n-1].End = s.End
			}
		} else {
			merged = append(merged, s)
		}
	}
	return merged, offlines
}

// ---- SVG chart ----

// fmtTS formats a unix timestamp in UTC ("01-02 15:04").
// This is only the no-JS fallback text; when JS is available the browser
// rewrites every [data-ts] element in the viewer's local timezone before
// first paint (see status.html), so the UTC text never flashes on screen.
func fmtTS(ts int64) string { return time.Unix(ts, 0).UTC().Format("01-02 15:04") }

// nextTick returns the first 6h axis tick at or after ts, aligned to UTC
// boundaries (00:00, 06:00, 12:00, 18:00 UTC). These ticks are only the
// no-JS fallback; the browser removes them and re-renders ticks aligned
// to the viewer's local timezone.
func nextTick(ts int64) int64 {
	t := time.Unix(ts, 0).UTC()
	y, m, d := t.Date()
	midnight := time.Date(y, m, d, 0, 0, 0, 0, time.UTC).Unix()
	for tick := midnight; ; tick += 21600 {
		if tick >= ts {
			return tick
		}
	}
}

func fmtDur(lang string, sec int64) string {
	if sec < 0 {
		sec = 0
	}
	h, rem := sec/3600, sec%3600
	m := rem / 60
	if lang == "zh" {
		switch {
		case h > 0:
			return fmt.Sprintf("%d小时%d分", h, m)
		case m > 0:
			return fmt.Sprintf("%d分", m)
		default:
			return fmt.Sprintf("%d秒", sec)
		}
	}
	switch {
	case h > 0:
		return fmt.Sprintf("%dh %dm", h, m)
	case m > 0:
		return fmt.Sprintf("%dm", m)
	default:
		return fmt.Sprintf("%ds", sec)
	}
}

func timelineSVG(lang string, segs []Segment, start, now int64) template.HTML {
	const width, height = 1440, 118
	span := float64(now - start)
	if span < 1 {
		span = 1
	}
	x := func(t int64) float64 { return float64(t-start) / span * width }

	var sb strings.Builder
	for _, s := range segs {
		if s.End <= s.Start {
			continue
		}
		x0 := x(s.Start)
		w := math.Max(x(s.End)-x0, 1.2)
		// Tooltip text is UTC here; the browser rewrites it in the viewer's
		// local timezone using the data-* attributes (see status.html).
		tipFmt := "%s: %s → %s (%s)"
		if lang == "zh" {
			tipFmt = "%s：%s → %s（%s）"
		}
		tip := fmt.Sprintf(tipFmt, stateName(lang, s.State), fmtTS(s.Start), fmtTS(s.End), fmtDur(lang, s.End-s.Start))
		fmt.Fprintf(&sb, `<rect x="%.1f" y="26" width="%.1f" height="34" fill="%s" data-state="%s" data-start="%d" data-end="%d" data-dur="%d"><title>%s</title></rect>`+"\n",
			x0, w, stateColors[s.State], stateName(lang, s.State), s.Start, s.End, s.End-s.Start, template.HTMLEscapeString(tip))
	}
	// Axis ticks every 6h, aligned to UTC boundaries (no-JS fallback).
	// The browser removes .tick elements and re-renders ticks aligned to
	// the viewer's local timezone.
	for t := nextTick(start); t <= now; t += 21600 {
		xt := x(t)
		fmt.Fprintf(&sb, `<line class="tick" x1="%.1f" y1="26" x2="%.1f" y2="66" stroke="#9ca3af"/>`, xt, xt)
		fmt.Fprintf(&sb, `<text class="tick" x="%.1f" y="82" font-size="12" text-anchor="middle" fill="#6b7280" data-ts="%d">%s</text>`+"\n", xt, t, fmtTS(t))
	}
	return template.HTML(fmt.Sprintf(
		`<svg viewBox="0 0 %d %d" width="100%%" style="background:#f9fafb;border:1px solid #e5e7eb;border-radius:8px" data-start="%d" data-now="%d">%s</svg>`,
		width, height, start, now, sb.String()))
}

// ---- system stats ----

func readStat() (total, idle uint64) {
	b, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0, 0
	}
	var vals []uint64
	for _, f := range strings.Fields(string(b))[1:] {
		v, err := strconv.ParseUint(f, 10, 64)
		if err != nil {
			break
		}
		vals = append(vals, v)
	}
	for _, v := range vals {
		total += v
	}
	if len(vals) > 4 {
		idle = vals[3] + vals[4] // idle + iowait
	}
	return total, idle
}

var (
	cachedCPUPct float64
	cachedCPUMu  sync.RWMutex
)

func init() {
	go func() {
		t1, i1 := readStat()
		for {
			time.Sleep(3 * time.Second)
			t2, i2 := readStat()
			dt, di := t2-t1, i2-i1
			t1, i1 = t2, i2
			if dt > 0 {
				pct := math.Round((1-float64(di)/float64(dt))*1000) / 10
				cachedCPUMu.Lock()
				cachedCPUPct = pct
				cachedCPUMu.Unlock()
			}
		}
	}()
}

func cpuPercent() float64 {
	cachedCPUMu.RLock()
	defer cachedCPUMu.RUnlock()
	return cachedCPUPct
}

func memInfo() (usedMB, totalMB int64, pct float64) {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0, 0
	}
	info := map[string]int64{}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		v, err := strconv.ParseInt(f[1], 10, 64)
		if err == nil {
			info[strings.TrimSuffix(f[0], ":")] = v // kB
		}
	}
	total := info["MemTotal"]
	if total == 0 {
		return 0, 0, 0
	}
	avail := info["MemAvailable"]
	if avail == 0 {
		avail = info["MemFree"]
	}
	used := total - avail
	return used / 1024, total / 1024, math.Round(float64(used)/float64(total)*1000) / 10
}

func diskInfo(path string) (usedGB, totalGB, freeGB, pct float64) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, 0, 0
	}
	const gb = 1024 * 1024 * 1024
	total := float64(st.Blocks) * float64(st.Bsize)
	free := float64(st.Bavail) * float64(st.Bsize)
	used := total - free
	r := func(v float64) float64 { return math.Round(v/gb*10) / 10 }
	pct = 0
	if total > 0 {
		pct = math.Round(used/total*1000) / 10
	}
	return r(used), r(total), r(free), pct
}

// ---- page rendering ----

type summaryRow struct {
	Name  string
	Color string
	Text  string
}

type eventRow struct {
	Start, End int64 // unix timestamps; rendered via data-ts + JS (UTC fallback text)
	Kind, Dur  string
}

type statusData struct {
	Lang      string
	Now       int64 // unix timestamp; rendered via data-ts + JS (UTC fallback text)
	CPU       float64
	MemUsed   int64
	MemTotal  int64
	MemPct    float64
	DiskUsed  float64
	DiskTotal float64
	DiskFree  float64
	DiskPct   float64
	DiskPath  string
	SVG       template.HTML
	Summary   []summaryRow
	Events    []eventRow
}

func renderStatus(w http.ResponseWriter, r *http.Request, tpl *template.Template, tl *Timeline, diskPath string) {
	lang := langOf(r)
	now := time.Now().Unix()
	segs, offlines := tl.buildSegments(now)
	start := now - windowSec

	totals := map[string]int64{}
	for _, s := range segs {
		if s.End > s.Start {
			totals[s.State] += s.End - s.Start
		}
	}
	var tot int64
	for _, v := range totals {
		tot += v
	}
	if tot == 0 {
		tot = 1
	}
	durFmt := "%.1f h (%.1f%%)"
	if lang == "zh" {
		durFmt = "%.1f 小时（%.1f%%）"
	}
	order := []string{"active", "offline", "unknown"}
	summary := make([]summaryRow, 0, len(order))
	for _, st := range order {
		sec := totals[st]
		summary = append(summary, summaryRow{
			Name:  stateName(lang, st),
			Color: stateColors[st],
			Text:  fmt.Sprintf(durFmt, float64(sec)/3600, float64(sec)/float64(tot)*100),
		})
	}

	type ev struct {
		start    int64
		kind     string
		dur      int64
	}
	var events []ev
	for _, o := range offlines {
		kind := T(lang, "event.offline")
		if o.Rebooted {
			kind = T(lang, "event.reboot")
		}
		events = append(events, ev{o.Start, kind, o.End - o.Start})
	}
	sort.Slice(events, func(i, j int) bool { return events[i].start > events[j].start })
	if len(events) > 30 {
		events = events[:30]
	}
	rows := make([]eventRow, 0, len(events))
	for _, e := range events {
		rows = append(rows, eventRow{
			Start: e.start, End: e.start + e.dur,
			Kind: e.kind, Dur: fmtDur(lang, e.dur),
		})
	}

	memUsed, memTotal, memPct := memInfo()
	diskUsed, diskTotal, diskFree, diskPct := diskInfo(diskPath)

	renderTemplate(w, tpl, "status.html", statusData{
		Lang:      lang,
		Now:       time.Now().Unix(),
		CPU:       cpuPercent(),
		MemUsed:   memUsed,
		MemTotal:  memTotal,
		MemPct:    memPct,
		DiskUsed:  diskUsed,
		DiskTotal: diskTotal,
		DiskFree:  diskFree,
		DiskPct:   diskPct,
		DiskPath:  diskPath,
		SVG:       timelineSVG(lang, segs, start, now),
		Summary:   summary,
		Events:    rows,
	})
}
