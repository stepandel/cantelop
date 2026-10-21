package main

import (
	"context"
	"database/sql"
	"flag"
	"github.com/stepandel/cantelop/fire-fuse/runtime/managedwatchdog"
	"log"
	"net"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

func main() {
	state := flag.String("state", "/var/lib/fire-fuse/state/state.db", "runtime SQLite state")
	groups := flag.String("cgroups", "/sys/fs/cgroup/system.slice/fire-fuse.service/sandboxes", "managed-only host sandbox cgroups")
	heartbeat := flag.String("heartbeat", "/run/fire-fuse-managed-watchdog/heartbeat", "private admission heartbeat")
	flag.Parse()
	if !filepath.IsAbs(*state) || !filepath.IsAbs(*groups) || !filepath.IsAbs(*heartbeat) {
		log.Fatal("absolute watchdog paths required")
	}
	u := url.URL{Scheme: "file", Path: *state}
	q := u.Query()
	q.Set("mode", "ro")
	q.Add("_pragma", "busy_timeout(1000)")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		log.Fatal("cannot open watchdog state")
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	w := managedwatchdog.Watchdog{DB: db, Cgroups: *groups, Heartbeat: *heartbeat}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	notify("READY=1")
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		step, cancelStep := context.WithTimeout(ctx, 1500*time.Millisecond)
		err = w.Step(step)
		cancelStep()
		if err != nil {
			log.Print("managed enforcement denied; retrying")
		}
		notify("WATCHDOG=1")
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func notify(message string) {
	path := os.Getenv("NOTIFY_SOCKET")
	if path == "" {
		return
	}
	if strings.HasPrefix(path, "@") {
		path = "\x00" + path[1:]
	}
	c, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: path, Net: "unixgram"})
	if err == nil {
		_, _ = c.Write([]byte(message))
		_ = c.Close()
	}
}
