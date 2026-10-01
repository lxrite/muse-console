package main

import (
	"encoding/json"
	"html/template"
	"log"
	"net/http"
	"time"
)

// utcFormats renders unix timestamps in UTC for the no-JS fallback.
// When JS is available, [data-ts] elements are rewritten in the
// viewer's local timezone before first paint (see status.html).
var utcFormats = template.FuncMap{
	"utcFull":  func(ts int64) string { return time.Unix(ts, 0).UTC().Format("2006-01-02 15:04:05") },
	"utcShort": func(ts int64) string { return time.Unix(ts, 0).UTC().Format("01-02 15:04") },
	// t looks up a UI string for the given language ("zh"/"en").
	"t": func(lang, key string) string { return T(lang, key) },
	// htmlLang maps "zh"->"zh-CN", "en"->"en" for the <html lang> attribute.
	"htmlLang": htmlLang,
	// i18nJSON embeds the message map for lang as a JS object
	// (used by the file browser's client-side strings).
	"i18nJSON": func(lang string) template.JS {
		m := messages[lang]
		if m == nil {
			m = messages["en"]
		}
		b, _ := json.Marshal(m)
		return template.JS(b)
	},
}

func mustParseTemplates() *template.Template {
	tpl, err := template.New("").Funcs(utcFormats).ParseFS(contentFS, "templates/*.html")
	if err != nil {
		log.Fatalf("parse templates: %v", err)
	}
	return tpl
}

func renderTemplate(w http.ResponseWriter, tpl *template.Template, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tpl.ExecuteTemplate(w, name, data); err != nil {
		log.Printf("render %s: %v", name, err)
	}
}

// pageData is the template data for pages that need nothing but the language.
type pageData struct {
	Lang string
}
