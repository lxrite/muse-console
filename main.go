// muse-console: single-binary web console for a VM.
//
//   - /      : status dashboard (time, CPU/mem/disk, 72h heartbeat timeline)
//   - /term  : web terminal (xterm.js + WebSocket + PTY)
//   - /files : file browser
//
// Authentication is username+password: a login form issues an HMAC-signed
// session cookie (API clients can use HTTP Basic Auth instead). All static
// assets are embedded via go:embed, so the build produces one
// self-contained executable.
package main

import (
	"embed"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"path/filepath"
)

var (
	version   = "dev"
	commit    = "none"
	buildDate = "unknown"
)

//go:embed static templates
var contentFS embed.FS

func main() {
	showVersion := flag.Bool("v", false, "print version and exit")
	flag.BoolVar(showVersion, "version", false, "print version and exit")
	configPath := flag.String("config", "config.toml", "TOML config file")
	flag.String("addr", "", "listen address (overrides config)")
	flag.String("data-dir", "", "directory holding timeline.jsonl (overrides config)")
	flag.String("disk-path", "", "path whose filesystem disk stats are shown (overrides config)")
	flag.String("files-root", "", "root directory for the file browser (overrides config)")
	flag.Parse()

	if *showVersion {
		fmt.Printf("muse-console %s (commit: %s, built: %s)\n", version, commit, buildDate)
		return
	}

	setFlags := map[string]*string{}
	flag.Visit(func(f *flag.Flag) {
		if s, ok := f.Value.(flag.Getter); ok {
			if str, ok := s.Get().(string); ok {
				v := str
				setFlags[f.Name] = &v
			}
		}
	})
	cfg := loadConfig(*configPath, setFlags)
	cfg.validate()

	auth := NewAuth(cfg.Users, *configPath, cfg.TrustedProxyHeaders)

	// Heartbeat timeline for the status dashboard.
	timeline := NewTimeline(filepath.Join(cfg.DataDir, "timeline.jsonl"))
	timeline.Load()
	go timeline.Run()

	staticSub, err := fs.Sub(contentFS, "static")
	if err != nil {
		log.Fatal(err)
	}
	tpl := mustParseTemplates()

	filesAPI := &FilesAPI{Root: cfg.FilesRoot}

	mux := http.NewServeMux()
	mux.HandleFunc("/login", auth.handleLogin(tpl))
	mux.HandleFunc("/logout", auth.handleLogout)
	mux.Handle("/change-password", auth.middleware(http.HandlerFunc(auth.handleChangePassword(tpl))))
	mux.Handle("/static/", auth.middleware(http.StripPrefix("/static/", http.FileServer(http.FS(staticSub)))))
	mux.Handle("/term", auth.middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		renderTemplate(w, tpl, "term.html", pageData{Lang: langOf(r)})
	})))
	mux.Handle("/files", auth.middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		renderTemplate(w, tpl, "files.html", pageData{Lang: langOf(r)})
	})))
	mux.Handle("/ws", http.HandlerFunc(auth.wsHandler)) // auth checked before upgrade
	mux.Handle("/api/files/list", auth.middleware(http.HandlerFunc(filesAPI.handleList)))
	mux.Handle("/api/files/download", auth.middleware(http.HandlerFunc(filesAPI.handleDownload)))
	mux.Handle("/api/files/view", auth.middleware(http.HandlerFunc(filesAPI.handleView)))
	mux.Handle("/api/files/upload", auth.middleware(http.HandlerFunc(filesAPI.handleUpload)))
	mux.Handle("/api/files/mkdir", auth.middleware(http.HandlerFunc(filesAPI.handleMkdir)))
	mux.Handle("/api/files/delete", auth.middleware(http.HandlerFunc(filesAPI.handleDelete)))
	mux.Handle("/api/files/rename", auth.middleware(http.HandlerFunc(filesAPI.handleRename)))
	mux.Handle("/", auth.middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		renderStatus(w, r, tpl, timeline, cfg.DiskPath)
	})))

	log.Printf("muse-console listening on http://%s", cfg.Addr)
	log.Printf("  dashboard : /")
	log.Printf("  terminal  : /term")
	log.Printf("  files     : /files (root %s)", cfg.FilesRoot)

	log.Fatal(http.ListenAndServe(cfg.Addr, mux))
}
