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
	"strings"
	"time"
)

// addr is where an app is served: the examples' main listens on :8080, and
// so does the workerd config structpages-workers writes.
const addr = "localhost:8080"

// serve is the serve command: one app served one way on addr until Ctrl-C.
func serve(args []string) int {
	if len(args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: structpages-dev serve folder reflect|generated|tinygo")
		return 2
	}
	f, way := newFolder(args[0]), args[1]
	s, err := start(f, way, os.Stdout, false)
	if err != nil {
		f.cleanGenerated()
		fail(err)
	}
	onInterrupt(func() { s.stop(); f.cleanGenerated() })
	fmt.Printf("%s, %s: http://%s%s\nCtrl-C stops it.\n", f.dir, way, addr, s.about)
	err = <-s.done
	s.stop()
	f.cleanGenerated()
	if err != nil {
		fmt.Fprintln(os.Stderr, "structpages-dev: the server stopped:", err)
		return 1
	}
	return 0
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

// start generates the code f needs the way asked, builds it and starts it on
// addr, its output to out. Every way builds into a temporary folder, deleted
// when it stops: go run uses its own, to serve; a benchmark (direct) builds
// into one of this run's and runs the binary directly, so stopping it stops
// the app itself, not just go run; a TinyGo build is a Worker run in
// workerd, since a TinyGo build can't listen on a socket.
func start(f folder, way string, out io.Writer, direct bool) (*server, error) {
	if c, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
		_ = c.Close()
		return nil, fmt.Errorf("%s is in use: stop what is on it first", addr)
	}
	if f.usesGSX() {
		if err := gsxGenerate(f, false); err != nil {
			return nil, err
		}
	}
	var tags []string
	switch way {
	case "reflect":
	case "generated", "tinygo":
		if err := generateWith(f, way == "tinygo", false); err != nil {
			return nil, err
		}
		if way == "generated" {
			tags = []string{"-tags", generatedTag}
		}
	default:
		return nil, fmt.Errorf("the way is reflect, generated or tinygo, not %q", way)
	}
	s := &server{done: make(chan error, 1)}
	if way != "reflect" {
		s.about = ", reading this generated code instead of reflect:" + listGenerated(f)
	}
	switch {
	case way == "tinygo":
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

// buildWorker builds f with TinyGo as a Worker into a temporary folder:
// workers-go's wasm_exec.js and runtime.mjs, structpages-workers' worker.mjs
// and config.capnp, and app.wasm. f must serve through workers.Serve.
func (s *server) buildWorker(f folder) error {
	if !f.servesWorkers() {
		return fmt.Errorf("%s can't run under TinyGo yet: it must serve through workers.Serve, as examples/todo does", f.dir)
	}
	tmp, err := os.MkdirTemp("", "structpages-dev-"+f.name+"-worker-")
	if err != nil {
		return err
	}
	s.tmp = tmp
	version, err := run(f.dir, "go", "list", "-m", "-f", "{{.Version}}", "github.com/syumai/workers-go")
	if err != nil {
		return fmt.Errorf("go list workers-go: %w\n%s", err, version)
	}
	steps := [][]string{
		{"go", "run", "github.com/syumai/workers-go/cmd/workers-assets-gen@" + strings.TrimSpace(version),
			"-mode", "tinygo", "-o", tmp},
		glueCommand(tmp),
		{"tinygo", "build", "-o", filepath.Join(tmp, "app.wasm"), "-target", "wasm", "-no-debug", "."},
	}
	for _, step := range steps {
		if o, rerr := run(f.dir, step[0], step[1:]...); rerr != nil {
			return fmt.Errorf("%s: %w\n%s", strings.Join(step[:2], " "), rerr, o)
		}
	}
	info, err := os.Stat(filepath.Join(tmp, "app.wasm"))
	if err != nil {
		return err
	}
	s.wasm = info.Size()
	return nil
}

// glueCommand writes structpages-workers' glue into out: from this
// repository's tools/workers, or elsewhere the published one.
func glueCommand(out string) []string {
	if root, ok := repoRoot(); ok {
		return []string{"go", "run", "-C", filepath.Join(root, "tools", "workers"), "./cmd/structpages-workers", "-o", out}
	}
	const published = "github.com/jackielii/structpages/tools/workers/cmd/structpages-workers@latest"
	return []string{"go", "run", published, "-o", out}
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
