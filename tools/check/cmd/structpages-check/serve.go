package main

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"time"
)

// addr is where an app is served: the examples' main listens on :8080, and
// so does the workerd config structpages-workers writes.
const addr = "localhost:8080"

// workerDir is where a TinyGo build is put together, inside the app's folder
// (gitignored).
const workerDir = "worker.generated"

// serve is the serve command: one app served one way on addr until Ctrl-C.
func serve(args []string) int {
	if len(args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: structpages-check serve folder reflect|generated|tinygo")
		return 2
	}
	list, err := folders(args[:1], nil)
	if err != nil {
		fail(err)
	}
	f, way := list[0], args[1]
	s, err := start(f, way, os.Stdout)
	if err != nil {
		f.cleanBuild()
		fail(err)
	}
	defer f.cleanBuild()
	defer s.stop()
	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt)
	fmt.Printf("%s, %s: http://%s%s\nCtrl-C stops it.\n", f.dir, way, addr, s.about)
	select {
	case <-interrupt:
		return 0
	case err := <-s.done:
		if err != nil {
			fmt.Fprintln(os.Stderr, "structpages-check: the server stopped:", err)
			return 1
		}
		return 0
	}
}

// server is an app being served.
type server struct {
	cmd   *exec.Cmd
	done  chan error // the server's exit, once
	bin   string     // the binary built for it, to delete, if any
	about string     // what it reads and runs as, for a banner
}

// stop stops the server, if it still runs, and deletes what was built for it.
func (s *server) stop() {
	if s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
		<-s.done
	}
	if s.bin != "" {
		_ = os.Remove(s.bin)
	}
}

// start builds f the way asked and starts it on addr, its output to out.
// The standard toolchain's builds are built to a temporary binary and run
// directly, so stopping it stops the app; a TinyGo build runs in workerd.
func start(f folder, way string, out io.Writer) (*server, error) {
	if c, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
		_ = c.Close()
		return nil, fmt.Errorf("%s is in use: stop what is on it first", addr)
	}
	if f.usesGSX() {
		if o, err := run(f.dir, "go", "tool", "gsx", "generate", "-q", "."); err != nil {
			return nil, fmt.Errorf("gsx generate: %w\n%s", err, o)
		}
	}
	s := &server{done: make(chan error, 1)}
	switch way {
	case "reflect", "generated":
		args := []string{"build", "-o"}
		if way == "generated" {
			if err := generateApp(f, false); err != nil {
				return nil, err
			}
			args = []string{"build", "-tags", generatedTag, "-o"}
			s.about = ", reading generated code instead of reflect:" + listGenerated(f)
		}
		bin, err := tempBinary(f.name)
		if err != nil {
			return nil, err
		}
		s.bin = bin
		if o, err := run(f.dir, "go", append(args, bin, ".")...); err != nil {
			_ = os.Remove(bin)
			return nil, fmt.Errorf("go build: %w\n%s", err, o)
		}
		s.cmd = exec.Command(bin) //nolint:gosec // the binary just built
	case "tinygo":
		about, err := buildWorker(f)
		if err != nil {
			return nil, err
		}
		s.about = about
		config := filepath.Join(workerDir, "config.capnp")
		s.cmd = exec.Command("workerd", "serve", config) //nolint:gosec // workerd, on the config just written
	default:
		return nil, fmt.Errorf("the way is reflect, generated or tinygo, not %q", way)
	}
	s.cmd.Dir, s.cmd.Stdout, s.cmd.Stderr = f.dir, out, out
	if err := s.cmd.Start(); err != nil {
		if s.bin != "" {
			_ = os.Remove(s.bin)
		}
		return nil, err
	}
	go func() { s.done <- s.cmd.Wait(); close(s.done) }()
	return s, nil
}

// generateApp writes an app's generated code: without -test, as it runs.
func generateApp(f folder, tinygo bool) error {
	return generateWith(f, tinygo, false)
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

// tempBinary is a path for a binary in the system's temporary directory.
func tempBinary(name string) (string, error) {
	t, err := os.CreateTemp("", "structpages-check-"+name+"-*")
	if err != nil {
		return "", err
	}
	_ = t.Close()
	return t.Name(), nil
}

// buildWorker builds f with TinyGo as a Worker into its worker.generated:
// workers-go's wasm_exec.js and runtime.mjs, structpages-workers' worker.mjs
// and config.capnp, and app.wasm. f must serve through the workers module.
func buildWorker(f folder) (string, error) {
	mod, _ := os.ReadFile(filepath.Join(f.dir, "go.mod"))
	if !strings.Contains(string(mod), "structpages/workers ") {
		return "", fmt.Errorf("%s can't run under TinyGo yet: it must serve through workers.Serve, as examples/todo does",
			f.dir)
	}
	if err := generateApp(f, true); err != nil {
		return "", err
	}
	out := filepath.Join(f.abs, workerDir)
	if err := os.RemoveAll(out); err != nil {
		return "", err
	}
	version, err := run(f.dir, "go", "list", "-m", "-f", "{{.Version}}", "github.com/syumai/workers-go")
	if err != nil {
		return "", fmt.Errorf("go list workers-go: %w\n%s", err, version)
	}
	steps := [][]string{
		{"go", "run", "github.com/syumai/workers-go/cmd/workers-assets-gen@" + strings.TrimSpace(version),
			"-mode", "tinygo", "-o", out},
		glueCommand(out),
		{"tinygo", "build", "-o", filepath.Join(out, "app.wasm"), "-target", "wasm", "-no-debug", "."},
	}
	for _, step := range steps {
		if o, rerr := run(f.dir, step[0], step[1:]...); rerr != nil {
			return "", fmt.Errorf("%s: %w\n%s", strings.Join(step[:2], " "), rerr, o)
		}
	}
	info, err := os.Stat(filepath.Join(out, "app.wasm"))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(", %d KB of WebAssembly in workerd, reading this generated code:%s",
		info.Size()/1024, listGenerated(f)), nil
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

// cleanBuild deletes what serving f built: its generated code and Worker.
func (f folder) cleanBuild() {
	f.cleanGenerated()
	_ = os.RemoveAll(filepath.Join(f.dir, workerDir))
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
