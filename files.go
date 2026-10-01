package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// FilesAPI serves a simple file browser jailed under Root.
// All paths in the API are slash-separated and relative to Root;
// anything resolving outside Root is rejected.
type FilesAPI struct {
	Root string
}

const (
	maxUploadBytes = 200 << 20 // 200 MB per upload request
	maxViewBytes   = 256 << 10 // 256 KB text preview
)

type fileEntry struct {
	Name string `json:"name"`
	Path string `json:"path"` // slash-separated, relative to root
	Dir  bool   `json:"dir"`
	Size int64  `json:"size"`
	Mod  int64  `json:"mod"` // unix timestamp
	Mode string `json:"mode"`
}

// resolve maps a client-supplied relative path to an absolute path and
// guarantees it stays inside the jail.
func (f *FilesAPI) resolve(reqPath string) (string, error) {
	clean := path.Clean("/" + reqPath) // always absolute, no ".." escapes "/"
	full := filepath.Join(f.Root, filepath.FromSlash(clean))
	rel, err := filepath.Rel(f.Root, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errForbidden
	}
	return full, nil
}

var errForbidden = fmt.Errorf("forbidden")

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func (f *FilesAPI) handleList(w http.ResponseWriter, r *http.Request) {
	lang := langOf(r)
	full, err := f.resolve(r.URL.Query().Get("path"))
	if err != nil {
		writeErr(w, http.StatusForbidden, T(lang, "api.path_escape"))
		return
	}
	entries, err := os.ReadDir(full)
	if err != nil {
		writeErr(w, http.StatusNotFound, T(lang, "api.cannot_list"))
		return
	}
	rel, _ := filepath.Rel(f.Root, full)
	relSlash := filepath.ToSlash(rel)
	out := make([]fileEntry, 0, len(entries))
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		p := e.Name()
		if relSlash != "." {
			p = relSlash + "/" + e.Name()
		}
		out = append(out, fileEntry{
			Name: e.Name(),
			Path: p,
			Dir:  e.IsDir(),
			Size: info.Size(),
			Mod:  info.ModTime().Unix(),
			Mode: info.Mode().String(),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Dir != out[j].Dir {
			return out[i].Dir
		}
		return out[i].Name < out[j].Name
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"path":    relSlash,
		"entries": out,
	})
}

func (f *FilesAPI) handleDownload(w http.ResponseWriter, r *http.Request) {
	lang := langOf(r)
	full, err := f.resolve(r.URL.Query().Get("path"))
	if err != nil {
		writeErr(w, http.StatusForbidden, T(lang, "api.path_escape"))
		return
	}
	st, err := os.Stat(full)
	if err != nil || st.IsDir() {
		writeErr(w, http.StatusNotFound, T(lang, "api.not_file"))
		return
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, filepath.Base(full)))
	http.ServeFile(w, r, full)
}

func (f *FilesAPI) handleView(w http.ResponseWriter, r *http.Request) {
	lang := langOf(r)
	full, err := f.resolve(r.URL.Query().Get("path"))
	if err != nil {
		writeErr(w, http.StatusForbidden, T(lang, "api.path_escape"))
		return
	}
	st, err := os.Stat(full)
	if err != nil || st.IsDir() {
		writeErr(w, http.StatusNotFound, T(lang, "api.not_file"))
		return
	}
	fh, err := os.Open(full)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, T(lang, "api.cannot_open"))
		return
	}
	defer fh.Close()
	buf := make([]byte, maxViewBytes+1)
	n, _ := io.ReadFull(fh, buf)
	for _, b := range buf[:n] {
		if b == 0 {
			writeErr(w, http.StatusBadRequest, T(lang, "api.not_text"))
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"content":   string(buf[:min(n, maxViewBytes)]),
		"truncated": n > maxViewBytes,
	})
}

func (f *FilesAPI) handleUpload(w http.ResponseWriter, r *http.Request) {
	lang := langOf(r)
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, T(lang, "api.post_only"))
		return
	}
	dir, err := f.resolve(r.URL.Query().Get("path"))
	if err != nil {
		writeErr(w, http.StatusForbidden, T(lang, "api.path_escape"))
		return
	}
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		writeErr(w, http.StatusBadRequest, T(lang, "api.not_dir"))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeErr(w, http.StatusBadRequest, T(lang, "api.cannot_parse")+err.Error())
		return
	}
	var saved []string
	for _, parts := range r.MultipartForm.File {
		for _, hdr := range parts {
			name := path.Base(hdr.Filename) // never trust client paths
			if name == "" || name == "." || name == "/" {
				continue
			}
			src, err := hdr.Open()
			if err != nil {
				continue
			}
			dst, err := os.OpenFile(filepath.Join(dir, name), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
			if err != nil {
				src.Close()
				continue
			}
			_, err = io.Copy(dst, src)
			dst.Close()
			src.Close()
			if err == nil {
				saved = append(saved, name)
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"saved": saved})
}

type pathReq struct {
	Path string `json:"path"`
}

func (f *FilesAPI) handleMkdir(w http.ResponseWriter, r *http.Request) {
	lang := langOf(r)
	var req pathReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Path == "" {
		writeErr(w, http.StatusBadRequest, T(lang, "api.missing_path"))
		return
	}
	full, err := f.resolve(req.Path)
	if err != nil {
		writeErr(w, http.StatusForbidden, T(lang, "api.path_escape"))
		return
	}
	if err := os.MkdirAll(full, 0755); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (f *FilesAPI) handleDelete(w http.ResponseWriter, r *http.Request) {
	lang := langOf(r)
	var req pathReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Path == "" {
		writeErr(w, http.StatusBadRequest, T(lang, "api.missing_path"))
		return
	}
	full, err := f.resolve(req.Path)
	if err != nil {
		writeErr(w, http.StatusForbidden, T(lang, "api.path_escape"))
		return
	}
	if rel, _ := filepath.Rel(f.Root, full); rel == "." {
		writeErr(w, http.StatusForbidden, T(lang, "api.del_root"))
		return
	}
	if err := os.RemoveAll(full); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (f *FilesAPI) handleRename(w http.ResponseWriter, r *http.Request) {
	lang := langOf(r)
	var req struct {
		From string `json:"from"`
		To   string `json:"to"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.From == "" || req.To == "" {
		writeErr(w, http.StatusBadRequest, T(lang, "api.missing_ft"))
		return
	}
	from, err := f.resolve(req.From)
	if err != nil {
		writeErr(w, http.StatusForbidden, T(lang, "api.path_escape"))
		return
	}
	to, err := f.resolve(req.To)
	if err != nil {
		writeErr(w, http.StatusForbidden, T(lang, "api.path_escape"))
		return
	}
	if err := os.Rename(from, to); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
