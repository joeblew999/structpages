// Command structpages-dev builds, tests, benchmarks and serves one folder of
// this repository (the structpages library, ., or an example, such as
// examples/todo) the three ways structpages runs: dynamic (the standard
// build, finding the app's methods at run time with reflect), static (the
// standard toolchain with -tags structpages_static, reading the code
// structpages-static generate writes instead) and static-worker (a Cloudflare Worker,
// TinyGo WebAssembly, which always reads that code):
//
//	structpages-dev test dynamic|static|static-worker
//	structpages-dev [-C dir] test [-ways dynamic,static,static-worker] folder
//	structpages-dev bench dynamic|static|static-worker
//	structpages-dev report
//	structpages-dev [-C dir] bench folder
//	structpages-dev serve dynamic|static|static-worker
//	structpages-dev [-C dir] serve folder dynamic|static|static-worker
//
// It is this repository's, run by its mise.toml, and it generates nothing:
// each folder's generate task there makes the folder's generated files first,
// in order, and the tasks running structpages-dev depend on it. Which folders
// to run it on is mise.toml's to say too. An app outside this repository uses
// the pieces directly: structpages-static generate, the e2e package, workers and
// structpages-workers (docs/static.md, docs/tinygo.md).
//
// test builds every package and runs the tests each way (with the race
// detector, which TinyGo lacks), and prints a line: how many passed each way,
// then every test skipped with why, and the failing tests' own output. bench
// runs the folder's benchmarks in-process and, for an app serving through
// workers.Serve, as served, and writes the folder's BENCH_GENERATED.md. serve
// serves an app on :8080 until Ctrl-C: go run on the standard toolchain, and
// under TinyGo a Worker in workerd, built into a temporary folder that
// stopping deletes.
package main

import (
	"bytes"
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
	dir := flag.String("C", "", "change to `dir` before anything else, as go -C does")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: structpages-dev test dynamic|static|static-worker\n"+
			"       structpages-dev [-C dir] test [-ways dynamic,static,static-worker] folder\n"+
			"       structpages-dev bench dynamic|static|static-worker\n"+
			"       structpages-dev report\n"+
			"       structpages-dev [-C dir] bench folder\n"+
			"       structpages-dev [-C dir] serve folder dynamic|static|static-worker")
		flag.PrintDefaults()
	}
	flag.Parse()
	if *dir != "" {
		if err := os.Chdir(*dir); err != nil {
			fail(err)
		}
	}
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

// servesWorkers reports whether f's module serves through workers.Serve,
// which a TinyGo build needs to run as a Worker.
func (f folder) servesWorkers() bool {
	mod, err := os.ReadFile(filepath.Join(f.dir, "go.mod"))
	return err == nil && bytes.Contains(mod, []byte("structpages/workers "))
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
