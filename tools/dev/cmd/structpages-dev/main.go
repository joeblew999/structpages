// Command structpages-dev builds, tests, benchmarks and serves one folder
// (the structpages library, ., or an app, such as examples/todo) the three
// ways structpages runs: reflect (the standard build), generated (the
// standard toolchain with -tags structpages_generated, reading the code
// structpages-gen writes instead of method reflection) and tinygo (which
// always reads that code):
//
//	structpages-dev [-C dir] test [-ways reflect,generated,tinygo] folder
//	structpages-dev [-C dir] bench folder
//	structpages-dev [-C dir] serve folder reflect|generated|tinygo
//
// Each job does the same steps every way: generate the code the way needs
// (generated and tinygo), build, then test, benchmark or serve, and delete
// what it generated and built when done. Which folders to run it on is the
// caller's to say: in this repository, mise.toml lists them.
//
// test builds every package and runs the tests (with the race detector,
// which TinyGo lacks), regenerating gsx output from scratch first and
// failing if the committed .x.go files are stale, and prints a line: how
// many passed each way, every test skipped with why, and the failing tests'
// own output. bench runs the folder's benchmarks in-process and, for an app
// serving through workers.Serve, as served, and writes the folder's
// BENCH_GENERATED.md. serve serves an app on :8080 until Ctrl-C: go run on
// the standard toolchain, and under TinyGo a Worker in workerd.
package main

import (
	"bufio"
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
)

// ways are the three ways, in the order they run and print.
var ways = []string{"reflect", "generated", "tinygo"}

const generatedTag = "structpages_generated"

func main() {
	dir := flag.String("C", "", "change to `dir` before anything else, as go -C does")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: structpages-dev [-C dir] test [-ways reflect,generated,tinygo] folder\n"+
			"       structpages-dev [-C dir] bench folder\n"+
			"       structpages-dev [-C dir] serve folder reflect|generated|tinygo")
		flag.PrintDefaults()
	}
	flag.Parse()
	if *dir != "" {
		if err := os.Chdir(*dir); err != nil {
			fail(err)
		}
	}
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
	if dir == "." {
		name = "library"
	}
	return folder{dir: dir, abs: abs, name: name}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// walk calls fn for each file in f, leaving out its examples and tools, which
// are folders of their own.
func (f folder) walk(fn func(path string)) {
	_ = filepath.WalkDir(f.dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && path != f.dir && (d.Name() == "examples" || d.Name() == "tools" || d.Name() == ".git") {
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

// usesGSX reports whether f's module has gsx as a tool.
func (f folder) usesGSX() bool {
	mod, err := os.ReadFile(filepath.Join(f.dir, "go.mod"))
	return err == nil && bytes.Contains(mod, []byte("gsxhq/gsx/cmd/gsx"))
}

// servesWorkers reports whether f's module serves through workers.Serve,
// which a TinyGo build needs to run as a Worker.
func (f folder) servesWorkers() bool {
	mod, err := os.ReadFile(filepath.Join(f.dir, "go.mod"))
	return err == nil && bytes.Contains(mod, []byte("structpages/workers "))
}

// cleanGenerated deletes the code structpages-gen wrote in f.
func (f folder) cleanGenerated() {
	f.walk(func(path string) {
		if base := filepath.Base(path); strings.HasPrefix(base, "structpages") && strings.Contains(base, ".generated") &&
			strings.HasSuffix(base, ".go") {
			_ = os.Remove(path)
		}
	})
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

// generate writes the code a way needs for f, with its tests' (-test), and
// the TinyGo files when tinygo is set.
func generate(f folder, tinygo bool) error { return generateWith(f, tinygo, true) }

// generateWith runs structpages-gen on f: in this repository from tools/gen,
// elsewhere the app's own go tool.
func generateWith(f folder, tinygo, tests bool) error {
	var args []string
	if tests {
		args = append(args, "-test")
	}
	if tinygo {
		args = append(args, "-tags", "tinygo")
	}
	var out string
	var err error
	if root, ok := repoRoot(); ok {
		gen := []string{"run", "-C", filepath.Join(root, "tools", "gen"), "./cmd/structpages-gen", "-C", f.abs}
		out, err = run(".", "go", append(gen, append(args, "./...")...)...)
	} else {
		out, err = run(f.dir, "go", append([]string{"tool", "structpages-gen"}, append(args, "./...")...)...)
	}
	// Its warnings (generic types it found no instantiation of) are not
	// failures; anything else it says is.
	var kept []string
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		if !strings.Contains(sc.Text(), "structpages-gen: warning") {
			kept = append(kept, sc.Text())
		}
	}
	if err != nil {
		return fmt.Errorf("structpages-gen: %w\n%s", err, strings.Join(kept, "\n"))
	}
	return nil
}

// repoRoot is the structpages repository the working directory is in, if
// it is: the folder with tools/gen.
func repoRoot() (string, bool) {
	dir, err := os.Getwd()
	if err != nil {
		return "", false
	}
	for {
		if fileExists(filepath.Join(dir, "tools", "gen", "go.mod")) {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

// gsxGenerate regenerates f's gsx output, from scratch when noCache is set.
func gsxGenerate(f folder, noCache bool) error {
	args := []string{"tool", "gsx", "generate", "-q"}
	if noCache {
		args = append(args, "-no-cache")
	}
	if out, err := run(f.dir, "go", append(args, ".")...); err != nil {
		return fmt.Errorf("gsx generate: %w\n%s", err, out)
	}
	return nil
}

// staleGSX regenerates f's gsx output from scratch and returns the committed
// .x.go files that changed.
func staleGSX(f folder) ([]string, error) {
	if err := gsxGenerate(f, true); err != nil {
		return nil, err
	}
	out, err := run(f.dir, "git", "status", "--porcelain", "--", ".")
	if err != nil {
		return nil, errors.New(out)
	}
	var stale []string
	for _, line := range strings.Split(out, "\n") {
		if strings.HasSuffix(strings.TrimSpace(line), ".x.go") {
			stale = append(stale, strings.TrimSpace(line))
		}
	}
	sort.Strings(stale)
	return stale, nil
}
