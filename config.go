package main

import (
	"log"
	"os"

	"github.com/BurntSushi/toml"
)

// Config is loaded from a TOML file; command-line flags override it.
// Example:
//
//	addr = "127.0.0.1:8080"
//	data_dir = "."
//	disk_path = "/home/hatch"   # shown on the dashboard; the persistent data disk
//	files_root = "/home/hatch"
//
//	[[users]]
//	username = "admin"
//	password = "change-me"   # plaintext; keep config.toml chmod 600
type Config struct {
	Addr       string `toml:"addr"`
	DataDir    string `toml:"data_dir"`
	DiskPath   string `toml:"disk_path"`
	FilesRoot  string `toml:"files_root"`
	// TrustedProxyHeaders lists request headers trusted for client-IP
	// detection in login rate limiting (e.g. behind a reverse proxy).
	// Empty (default) means: use the direct TCP peer. Only set this behind
	// a TRUSTED proxy — otherwise anyone can spoof the header and bypass
	// the brute-force limit.
	TrustedProxyHeaders []string `toml:"trusted_proxy_headers"`
	Users               []User   `toml:"users"`
}

type User struct {
	Username string `toml:"username"`
	Password string `toml:"password"`
}

func defaultConfig() *Config {
	return &Config{
		Addr:      "127.0.0.1:8080",
		DataDir:   ".",
		DiskPath:  "/home/hatch",
		FilesRoot: "/",
	}
}

// loadConfig reads the TOML file (if it exists) and applies explicitly-set
// flags on top. Precedence: flags > config file > defaults.
func loadConfig(path string, setFlags map[string]*string) *Config {
	cfg := defaultConfig()
	if b, err := os.ReadFile(path); err == nil {
		if err := toml.Unmarshal(b, cfg); err != nil {
			log.Fatalf("parse config %s: %v", path, err)
		}
	} else if !os.IsNotExist(err) {
		log.Fatalf("read config %s: %v", path, err)
	}
	if v, ok := setFlags["addr"]; ok {
		cfg.Addr = *v
	}
	if v, ok := setFlags["data-dir"]; ok {
		cfg.DataDir = *v
	}
	if v, ok := setFlags["disk-path"]; ok {
		cfg.DiskPath = *v
	}
	if v, ok := setFlags["files-root"]; ok {
		cfg.FilesRoot = *v
	}
	return cfg
}

func (c *Config) validate() {
	if len(c.Users) == 0 {
		log.Fatal("config: no [[users]] defined — add at least one user with a password")
	}
	seen := map[string]bool{}
	for _, u := range c.Users {
		if u.Username == "" || u.Password == "" {
			log.Fatal("config: each [[users]] needs username and password")
		}
		if seen[u.Username] {
			log.Fatalf("config: duplicate username %q", u.Username)
		}
		seen[u.Username] = true
	}
	if st, err := os.Stat(c.FilesRoot); err != nil || !st.IsDir() {
		log.Fatalf("files_root is not a directory: %s", c.FilesRoot)
	}
}
