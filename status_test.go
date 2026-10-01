package main

import (
	"testing"
	"time"
)

func fptr(f float64) *float64 { return &f }

func rec(ts int64, boot string, up float64) Record {
	return Record{TS: ts, Boot: boot, Up: fptr(up)}
}

func TestDisplayTimezone(t *testing.T) {
	// Times are always rendered in UTC by the server now; the browser
	// converts them to the viewer's local timezone via JS.
	// 2026-09-30 14:48:00 UTC.
	ts := time.Date(2026, 9, 30, 14, 48, 0, 0, time.UTC).Unix()

	if got := fmtTS(ts); got != "09-30 14:48" {
		t.Errorf("fmtTS UTC: got %q, want %q", got, "09-30 14:48")
	}

	// Axis ticks align to UTC 6h boundaries.
	// 14:48 UTC -> next tick is 18:00 UTC.
	if got, want := nextTick(ts), time.Date(2026, 9, 30, 18, 0, 0, 0, time.UTC).Unix(); got != want {
		t.Errorf("nextTick: got %d, want %d", got, want)
	}
	// Exactly on a boundary -> that boundary itself.
	onTick := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC).Unix()
	if got := nextTick(onTick); got != onTick {
		t.Errorf("nextTick on boundary: got %d, want %d", got, onTick)
	}
}

func TestRebooted(t *testing.T) {
	cases := []struct {
		name string
		a, b Record
		want bool
	}{
		{"boot change", rec(1, "aaa", 100), rec(2, "bbb", 50), true},
		{"uptime regression", rec(1, "aaa", 1000), rec(2, "aaa", 20), true},
		{"normal", rec(1, "aaa", 1000), rec(2, "aaa", 1060), false},
		{"missing info", Record{TS: 1}, Record{TS: 2, Boot: "aaa"}, false},
		{"small uptime jitter", rec(1, "aaa", 1000), rec(2, "aaa", 998), false},
	}
	for _, c := range cases {
		if got := rebooted(c.a, c.b); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestBuildSegments(t *testing.T) {
	now := int64(1700000000)
	mk := func() *Timeline {
		tl := NewTimeline("")
		tl.records = []Record{
			rec(now-1200, "aaa", 1000),
			rec(now-1140, "aaa", 1060),
			rec(now-540, "aaa", 1120), // 600s gap, same boot, no marker -> offline
			rec(now-480, "aaa", 1180),
			rec(now-300, "bbb", 50), // boot change, 180s gap -> offline (rebooted)
			rec(now-240, "bbb", 110),
			rec(now-120, "bbb", 200), // 120s gap, same boot -> offline
			rec(now-30, "bbb", 290),
		}
		return tl
	}

	tl := mk()
	segs, offs := tl.buildSegments(now)

	seen := map[string]bool{}
	for _, s := range segs {
		if s.End-s.Start > gapSec || s.State != "active" {
			seen[s.State] = true
		}
		// "unknown" is only allowed for the leading no-data segment
		if s.State == "unknown" && s.Start != now-windowSec {
			t.Errorf("gap segment should not be unknown, got %+v", s)
		}
	}
	if !seen["offline"] {
		t.Errorf("expected an offline segment, got %+v", segs)
	}
	if seen["hibernated"] || seen["shutdown"] {
		t.Errorf("hibernated/shutdown states should be gone, got %+v", segs)
	}
	if len(offs) != 3 {
		t.Fatalf("expected 3 offline events, got %d", len(offs))
	}
	// boot-change gap -> Rebooted, others -> plain heartbeat loss
	var rebootedCount int
	for _, o := range offs {
		if o.Rebooted {
			rebootedCount++
		}
	}
	if rebootedCount != 1 {
		t.Errorf("expected 1 rebooted offline event, got %+v", offs)
	}
}

func TestBuildSegmentsEmpty(t *testing.T) {
	tl := NewTimeline("")
	segs, _ := tl.buildSegments(1700000000)
	if len(segs) != 1 || segs[0].State != "unknown" {
		t.Errorf("empty timeline should yield one unknown segment, got %+v", segs)
	}
}

func TestResolveJail(t *testing.T) {
	f := &FilesAPI{Root: "/tmp/mctest/root"}
	for _, p := range []string{"../..", "/etc", "%2e%2e/%2e%2e", "a/../../b", ""} {
		full, err := f.resolve(p)
		if err != nil {
			t.Errorf("resolve(%q) unexpectedly rejected: %v", p, err)
		}
		// must stay inside root: filepath.Join cleans, so check prefix
		if len(full) < len(f.Root) || full[:len(f.Root)] != f.Root {
			t.Errorf("resolve(%q) escaped jail: %s", p, full)
		}
	}
	if _, err := f.resolve("/"); err != nil {
		t.Errorf("resolve(/) should map to root: %v", err)
	}
}
