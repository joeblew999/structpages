// Command structpages-dev is this repository's dev tool, which its mise.toml
// runs: each command is one step, on the folder it runs in (the structpages
// library, or an example), for one of the three ways structpages runs:
// dynamic (the standard build, finding the app's methods at run time with
// reflect), static (the standard toolchain with -tags structpages_static,
// reading the code structpages-static generate writes instead) and
// static-worker (a Cloudflare Worker, TinyGo WebAssembly, which always reads
// that code):
//
//	structpages-dev test dynamic|static|static-worker
//	structpages-dev bench dynamic|static|static-worker [build]
//	structpages-dev bench floor
//	structpages-dev report
//	structpages-dev serve dynamic|static|static-worker
//	structpages-dev floor
//
// It generates and builds nothing an app needs: mise.toml's generate and
// build tasks do, and the tasks running structpages-dev depend on them, so the
// order of the steps is mise.toml's to say. An app outside this repository
// uses the pieces directly: structpages-static generate, the e2e package,
// workers and structpages-workers (docs/static.md, docs/tinygo.md).
//
// test builds every package and runs the tests that way (with the race
// detector, but under TinyGo, which lacks it), and prints a line: how many
// passed, every test skipped with why, and the failing tests' own output.
// bench times the folder's benchmarks that way, in-process and, for an app,
// its build served, into build/bench/; bench floor times the two servers the
// ways are served by, Go's net/http and workerd, answering the app's
// recorded pages with no app behind them; and report compares what it finds
// there with benchstat into the folder's BENCH_GENERATED.md, with each way's
// own CPU, its build's less its server's. serve runs the folder's build for a
// way, build/<way>, on :8080 until Ctrl-C: in workerd for static-worker.
// floor serves the recorded pages with net/http, no app behind them, until
// stopped: bench floor runs it.
package main

import (
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
)

// ways are the three ways, in the order they run and print.
var ways = []string{"dynamic", "static", "static-worker"}

const staticTag = "structpages_static"

// root is the repository's root folder, the one with tools/static.
var root string

func main() {
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: structpages-dev test dynamic|static|static-worker\n"+
			"       structpages-dev bench dynamic|static|static-worker [build]\n"+
			"       structpages-dev bench floor\n"+
			"       structpages-dev report\n"+
			"       structpages-dev serve dynamic|static|static-worker\n"+
			"       structpages-dev floor")
	}
	flag.Parse()
	r, ok := repoRoot()
	if !ok {
		fail(fmt.Errorf("it runs inside the structpages repository, from its mise.toml; " +
			"an app of your own uses structpages-static generate and the e2e package directly"))
	}
	root = r
	if flag.NArg() == 0 {
		flag.Usage()
		os.Exit(2)
	}
	switch cmd, args := flag.Arg(0), flag.Args()[1:]; cmd {
	case "test":
		os.Exit(test(args))
	case "bench":
		os.Exit(bench(args))
	case "serve":
		os.Exit(serve(args))
	case "report":
		os.Exit(report(args))
	case "floor":
		os.Exit(floor(args))
	default:
		fmt.Fprintf(os.Stderr, "structpages-dev: unknown command %q\n", cmd)
		flag.Usage()
		os.Exit(2)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "structpages-dev:", err)
	os.Exit(2)
}

// folder is the folder a job runs on: the library, or an app.
type folder struct {
	dir  string // as given, relative to the working directory
	abs  string
	name string // "library", or the app's folder name
}

func newFolder(dir string) folder {
	dir = filepath.Clean(dir)
	abs, err := filepath.Abs(dir)
	if err != nil {
		fail(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "go.mod")); err != nil {
		fail(fmt.Errorf("%s: no go.mod: a folder is the library (.) or an app's module", dir))
	}
	name := filepath.Base(abs)
	if abs == root {
		name = "library"
	}
	return folder{dir: dir, abs: abs, name: name}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// walk calls fn for each file in f, leaving out the folders with a go.mod of
// their own (the examples, tools and workers), which are other modules,
// as go's ./... does.
func (f folder) walk(fn func(path string)) {
	_ = filepath.WalkDir(f.dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && path != f.dir && (d.Name() == ".git" || fileExists(filepath.Join(path, "go.mod"))) {
			return filepath.SkipDir
		}
		if !d.IsDir() {
			fn(path)
		}
		return nil
	})
}

// hasTests reports whether f has a _test.go file.
func (f folder) hasTests() bool {
	found := false
	f.walk(func(path string) { found = found || strings.HasSuffix(path, "_test.go") })
	return found
}

// onInterrupt runs cleanup and exits if the run is interrupted (Ctrl-C).
func onInterrupt(cleanup func()) {
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)
	go func() {
		<-c
		cleanup()
		os.Exit(130)
	}()
}

// run runs name with args in dir and returns its output, stdout and stderr
// together.
func run(dir, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// repoRoot is the structpages repository the working directory is in, if
// it is: the folder with tools/static.
func repoRoot() (string, bool) {
	dir, err := os.Getwd()
	if err != nil {
		return "", false
	}
	for {
		if fileExists(filepath.Join(dir, "tools", "static", "go.mod")) {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}
