package main

import "testing"

func TestDetectLang(t *testing.T) {
	cases := []struct {
		header string
		want   string
	}{
		{"zh-CN,zh;q=0.9,en;q=0.8", "zh"},
		{"zh-TW,zh;q=0.9", "zh"},          // Traditional -> Simplified (best match)
		{"zh-HK", "zh"},
		{"en-US,en;q=0.9", "en"},
		{"en-US,zh-CN;q=0.5", "en"},        // q-value order respected
		{"zh-CN;q=0.5,en-US;q=0.9", "en"},  // q-value order respected
		{"fr-FR,de-DE", "en"},              // unsupported -> English fallback
		{"", "en"},                         // empty -> English fallback
		{"*", "en"},
		{"ja-JP,zh-CN;q=0.8", "zh"},        // first supported wins
	}
	for _, c := range cases {
		if got := detectLang(c.header); got != c.want {
			t.Errorf("detectLang(%q) = %q, want %q", c.header, got, c.want)
		}
	}
}

func TestTFallback(t *testing.T) {
	if got := T("zh", "nav.terminal"); got != "终端" {
		t.Errorf("T(zh, nav.terminal) = %q", got)
	}
	if got := T("en", "nav.terminal"); got != "Terminal" {
		t.Errorf("T(en, nav.terminal) = %q", got)
	}
	if got := T("fr", "nav.terminal"); got != "Terminal" {
		t.Errorf("T(fr, nav.terminal) = %q, want English fallback", got)
	}
	if got := T("zh", "nonexistent.key"); got != "nonexistent.key" {
		t.Errorf("T missing key = %q, want key itself", got)
	}
}
