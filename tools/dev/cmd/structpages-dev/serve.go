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

// serve is the serve command. With a way, it is one step: the current
// folder's build for that way, build/<way>, which its build task made,
// served on addr until Ctrl-C. With a folder and a way, it builds the folder
// that way first, into a temporary folder.
func serve(args []string) int {
	if len(args) == 1 && slices.Contains(ways, args[0]) {
		return serveBuild(newFolder("."), args[0])
	}
	if len(args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: structpages-dev serve folder dynamic|static|static-worker")
		return 2
	}
	f, way := newFolder(args[0]), args[1]
	s, err := start(f, way, os.Stdout, false)
	if err != nil {
		fail(err)
	}
	onInterrupt(s.stop)
	fmt.Printf("%s, %s: http://%s%s\nCtrl-C stops it.\n", f.dir, way, addr, s.about)
	err = <-s.done
	s.stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, "structpages-dev: the server stopped:", err)
		return 1
	}
	return 0
}

// serveBuild runs f's build for way, build/<way>, on addr until Ctrl-C.
func serveBuild(f folder, way string) int {
	s, err := startBuild(f, way, os.Stdout)
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
// build task made, build/<way>, a binary, or for static-worker a Worker's
// folder, run in workerd.
func startBuild(f folder, way string, out io.Writer) (*server, error) {
	built := filepath.Join(f.abs, "build", way)
	cmd := exec.Command(built) //nolint:gosec // the folder's own build
	if way == "static-worker" {
		cmd = exec.Command("workerd", "serve", filepath.Join(built, "config.capnp")) //nolint:gosec // its own build
		built = filepath.Join(built, "app.wasm")
	}
	info, err := os.Stat(built)
	if err != nil {
		return nil, fmt.Errorf("%s isn't built: build it first (mise run example:%s:build:%s)", built, f.name, way)
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

// server is an app being served.
type server struct {
	cmd   *exec.Cmd
	done  chan error // the server's exit, once
	tmp   string     // the temporary folder its build is in, if any
	wasm  int64      // a TinyGo build's app.wasm size
	about string     // what it reads and runs as, for a banner
}

// stop stops the server, if it still runs, and deletes its build.
func (s *server) stop() {
	if s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
		<-s.done
	}
	if s.tmp != "" {
		_ = os.RemoveAll(s.tmp)
	}
}

// start builds f the way asked and starts it on addr, its output to out, from
// the generated files f's generate task made. Every way builds into a
// temporary folder, deleted
// when it stops: go run uses its own, to serve; a benchmark (direct) builds
// into one of this run's and runs the binary directly, so stopping it stops
// the app itself, not just go run; a TinyGo build is a Worker run in
// workerd, since a TinyGo build can't listen on a socket.
func start(f folder, way string, out io.Writer, direct bool) (*server, error) {
	if c, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
		_ = c.Close()
		return nil, fmt.Errorf("%s is in use: stop what is on it first", addr)
	}
	var tags []string
	switch way {
	case "dynamic", "static-worker":
	case "static":
		tags = []string{"-tags", staticTag}
	default:
		return nil, fmt.Errorf("the way is dynamic, static or static-worker, not %q", way)
	}
	s := &server{done: make(chan error, 1)}
	if way != "dynamic" {
		s.about = ", reading this generated code instead of reflect:" + listGenerated(f)
	}
	switch {
	case way == "static-worker":
		if err := s.buildWorker(f); err != nil {
			return nil, err
		}
		s.cmd = exec.Command("workerd", "serve", filepath.Join(s.tmp, "config.capnp")) //nolint:gosec // its own build
		s.about = fmt.Sprintf(", %d KB of WebAssembly in workerd%s", s.wasm/1024, s.about)
	case direct:
		tmp, err := os.MkdirTemp("", "structpages-dev-"+f.name+"-")
		if err != nil {
			return nil, err
		}
		s.tmp = tmp
		bin := filepath.Join(tmp, "app")
		if o, err := run(f.dir, "go", append(append([]string{"build"}, tags...), "-o", bin, ".")...); err != nil {
			s.stop()
			return nil, fmt.Errorf("go build: %w\n%s", err, o)
		}
		s.cmd = exec.Command(bin) //nolint:gosec // the binary just built
	default:
		s.cmd = exec.Command("go", append(append([]string{"run"}, tags...), ".")...) //nolint:gosec // go, on the folder asked
	}
	s.cmd.Dir, s.cmd.Stdout, s.cmd.Stderr = f.dir, out, out
	if err := s.cmd.Start(); err != nil {
		s.stop()
		return nil, err
	}
	go func() { s.done <- s.cmd.Wait(); close(s.done) }()
	return s, nil
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

// buildWorker builds f with TinyGo as a Worker into a temporary folder, as
// its build:static-worker task does into build/static-worker: with its own
// go tool structpages-workers. f must serve through workers.Serve.
func (s *server) buildWorker(f folder) error {
	if !f.servesWorkers() {
		return fmt.Errorf("%s can't run under TinyGo yet: it must serve through workers.Serve, as examples/todo does", f.dir)
	}
	tmp, err := os.MkdirTemp("", "structpages-dev-"+f.name+"-worker-")
	if err != nil {
		return err
	}
	s.tmp = tmp
	if o, rerr := run(f.dir, "go", "tool", "structpages-workers", "-o", tmp); rerr != nil {
		return fmt.Errorf("structpages-workers: %w\n%s", rerr, o)
	}
	info, err := os.Stat(filepath.Join(tmp, "app.wasm"))
	if err != nil {
		return err
	}
	s.wasm = info.Size()
	return nil
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
