// Command structpages-check builds and tests the structpages library or an
// app the three ways it runs: reflect (the standard build), generated (the
// standard toolchain with -tags structpages_generated, reading the code
// structpages-gen writes instead of method reflection) and tinygo (which
// always reads that code):
//
//	structpages-check [-C dir] test [-ways reflect,generated,tinygo] [-skip name,...] folder...
//
// A folder is the library (.) or an app (examples/todo); one ending in /...
// is every folder under it with a go.mod. For each folder, test regenerates
// gsx output from scratch and fails if the committed .x.go files are stale,
// then, each way, generates the code that way needs, builds every package and
// runs the tests, and prints a line: how many passed each way, every test
// skipped with why, and the first lines of any failure. The code it generates
// goes when it is done.
//
// It is its own module, apart from package check, which apps' tests import:
// that one must stay small enough to build under TinyGo.
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
	"slices"
	"sort"
	"strings"
)

// ways are the three ways, in the order they run and print.
var ways = []string{"reflect", "generated", "tinygo"}

const generatedTag = "structpages_generated"

func main() {
	dir := flag.String("C", "", "change to `dir` before anything else, as go -C does")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr,
			"usage: structpages-check [-C dir] test [-ways reflect,generated,tinygo] [-skip name,...] folder...\n"+
				"       structpages-check [-C dir] bench folder...\n"+
				"       structpages-check [-C dir] serve folder reflect|generated|tinygo")
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
		fmt.Fprintf(os.Stderr, "structpages-check: unknown command %q\n", cmd)
		flag.Usage()
		os.Exit(2)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "structpages-check:", err)
	os.Exit(2)
}

// folder is one folder to check: the library, or an app.
type folder struct {
	dir  string // as given, relative to the working directory
	abs  string
	name string // "library", or the app's folder name
}

// folders expands the folders given: dir/... is every folder under dir with
// a go.mod, sorted, less those named in skip.
func folders(args, skip []string) ([]folder, error) {
	var dirs []string
	for _, a := range args {
		base, many := strings.CutSuffix(filepath.ToSlash(a), "/...")
		if !many {
			dirs = append(dirs, filepath.Clean(a))
			continue
		}
		entries, err := os.ReadDir(base)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if e.IsDir() && fileExists(filepath.Join(base, e.Name(), "go.mod")) && !slices.Contains(skip, e.Name()) {
				dirs = append(dirs, filepath.Join(base, e.Name()))
			}
		}
	}
	list := make([]folder, 0, len(dirs))
	for _, d := range dirs {
		abs, err := filepath.Abs(d)
		if err != nil {
			return nil, err
		}
		name := filepath.Base(abs)
		if d == "." {
			name = "library"
		}
		list = append(list, folder{dir: d, abs: abs, name: name})
	}
	return list, nil
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

// cleanGenerated deletes the code structpages-gen wrote in f.
func (f folder) cleanGenerated() {
	f.walk(func(path string) {
		if base := filepath.Base(path); strings.HasPrefix(base, "structpages") && strings.Contains(base, ".generated") &&
			strings.HasSuffix(base, ".go") {
			_ = os.Remove(path)
		}
	})
}

// cleanOnInterrupt deletes the generated code in list if the run is
// interrupted, as it would when the run ends.
func cleanOnInterrupt(list []folder) {
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)
	go func() {
		<-c
		for _, f := range list {
			f.cleanGenerated()
		}
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

// staleGSX regenerates f's gsx output from scratch and returns the committed
// .x.go files that changed.
func staleGSX(f folder) ([]string, error) {
	if out, err := run(f.dir, "go", "tool", "gsx", "generate", "-q", "-no-cache", "."); err != nil {
		return nil, fmt.Errorf("gsx generate: %w\n%s", err, out)
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
