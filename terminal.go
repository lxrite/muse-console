package main

import (
	"encoding/base64"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"os/exec"

	"github.com/coder/websocket"
	"github.com/creack/pty"
)

// wire message: {"t":"in","d":"<base64>"} | {"t":"resize","cols":N,"rows":M}
//               {"t":"out","d":"<base64>"}
type wireMsg struct {
	T    string `json:"t"`
	D    string `json:"d,omitempty"`
	Cols int    `json:"cols,omitempty"`
	Rows int    `json:"rows,omitempty"`
}

func (a *Auth) wsHandler(w http.ResponseWriter, r *http.Request) {
	if _, ok, _ := a.authenticate(r); !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: []string{r.Host},
	})
	if err != nil {
		log.Printf("ws accept: %v", err)
		return
	}
	defer c.Close(websocket.StatusNormalClosure, "")
	ctx := r.Context()

	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/bash"
	}
	cmd := exec.Command(shell)
	if dir := r.URL.Query().Get("dir"); dir != "" {
		if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
			cmd.Dir = dir
		}
	}
	if cmd.Dir == "" {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			cmd.Dir = home
		}
	}
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	ptmx, err := pty.Start(cmd)
	if err != nil {
		log.Printf("pty start: %v", err)
		return
	}
	defer func() {
		cmd.Process.Kill()
		ptmx.Close()
	}()
	// initial size; frontend sends a resize right after open anyway
	_ = pty.Setsize(ptmx, &pty.Winsize{Cols: 80, Rows: 24})

	// pty -> websocket
	go func() {
		buf := make([]byte, 8192)
		for {
			n, err := ptmx.Read(buf)
			if err != nil {
				return
			}
			m, _ := json.Marshal(wireMsg{
				T: "out",
				D: base64.StdEncoding.EncodeToString(buf[:n]),
			})
			if err := c.Write(ctx, websocket.MessageText, m); err != nil {
				return
			}
		}
	}()

	// websocket -> pty
	for {
		_, data, err := c.Read(ctx)
		if err != nil {
			return
		}
		var m wireMsg
		if err := json.Unmarshal(data, &m); err != nil {
			continue
		}
		switch m.T {
		case "in":
			if raw, err := base64.StdEncoding.DecodeString(m.D); err == nil {
				ptmx.Write(raw)
			}
		case "resize":
			if m.Cols > 0 && m.Rows > 0 && m.Cols < 1000 && m.Rows < 1000 {
				_ = pty.Setsize(ptmx, &pty.Winsize{
					Cols: uint16(m.Cols), Rows: uint16(m.Rows),
				})
			}
		}
	}
}
