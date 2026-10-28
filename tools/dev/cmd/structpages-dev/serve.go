package main

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// addr is where an app is served: the examples' main listens on :8080, and
// so does the workerd config structpages-workers writes.
const addr = "localhost:8080"

// serve is the serve command, one step: the current folder's build for a
// way, build/<way>, which its build task made, served on addr until Ctrl-C.
func serve(args []string) int {
	if len(args) != 1 || !slices.Contains(ways, args[0]) {
		fmt.Fprintln(os.Stderr, "usage: structpages-dev serve dynamic|static|static-worker")
		return 2
	}
	return serveBuild(newFolder("."), args[0])
}

// serveBuild runs f's build for way, build/<way>, on addr until Ctrl-C.
func serveBuild(f folder, way string) int {
	s, err := startBuild(f, way, way, os.Stdout)
	if err != nil {
		fail(err)
	}
	onInterrupt(s.stop)
	about := ""
	if way != "dynamic" {
		about = ", reading this generated code instead of reflect:" + listGenerated(f)
	}
	if s.wasm > 0 {
		about = fmt.Sprintf(", %d KB of WebAssembly in workerd%s", s.wasm/1024, about)
	}
	fmt.Printf("%s, %s: http://%s%s\nCtrl-C stops it.\n", f.name, way, addr, about)
	err = <-s.done
	s.stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, "structpages-dev: the server stopped:", err)
		return 1
	}
	return 0
}

// startBuild starts f's build for way on addr, its output to out: what its
// build task made, build/<build> (build/<way>, or a variant's folder), a
// binary, or for static-worker a Worker's folder, run in workerd.
func startBuild(f folder, way, build string, out io.Writer) (*server, error) {
	built := filepath.Join(f.abs, "build", build)
	cmd := exec.Command(built) //nolint:gosec // the folder's own build
	if way == "static-worker" {
		cmd = exec.Command("workerd", "serve", filepath.Join(built, "config.capnp")) //nolint:gosec // its own build
		built = filepath.Join(built, "app.wasm")
	}
	info, err := os.Stat(built)
	if err != nil {
		return nil, fmt.Errorf("%s isn't built: build it first (mise run example:%s:build:%s)", built, f.name, build)
	}
	if c, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
		_ = c.Close()
		return nil, fmt.Errorf("%s is in use: stop what is on it first", addr)
	}
	s := &server{cmd: cmd, done: make(chan error, 1)}
	if way == "static-worker" {
		s.wasm = info.Size()
	}
	cmd.Dir, cmd.Stdout, cmd.Stderr = f.dir, out, out
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	go func() { s.done <- cmd.Wait(); close(s.done) }()
	return s, nil
}

// server is a folder's build being served.
type server struct {
	cmd  *exec.Cmd
	done chan error // the server's exit, once
	wasm int64      // a static-worker build's app.wasm size
}

// stop stops the server, if it still runs.
func (s *server) stop() {
	if s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
		<-s.done
	}
}

// listGenerated is the generated files in f, a line each, for a banner.
func listGenerated(f folder) string {
	var b strings.Builder
	f.walk(func(path string) {
		if base := filepath.Base(path); strings.HasPrefix(base, "structpages") && strings.Contains(base, ".generated") {
			rel, _ := filepath.Rel(f.dir, path)
			b.WriteString("\n  " + rel)
		}
	})
	return b.String()
}

// waitUp waits for the app on addr to answer, as a build can take a while.
func waitUp(s *server, limit time.Duration) error {
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		select {
		case err := <-s.done:
			return fmt.Errorf("the server stopped: %v", err)
		case <-time.After(250 * time.Millisecond):
		}
		if resp, err := http.Get("http://" + addr + "/"); err == nil { //nolint:noctx // a local health check
			_ = resp.Body.Close()
			return nil
		}
	}
	return errors.New("the server never answered")
}
