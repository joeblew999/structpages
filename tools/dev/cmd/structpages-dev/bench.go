package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// benchFlags are every benchmark's, 6 runs of 200ms each: in-process with
// -benchmem; as served, times only (the allocations would be the client's).
var (
	benchFlags  = []string{"-run", "NoTest", "-bench", ".", "-count", "6", "-benchtime", "200ms"}
	benchmemArg = "-benchmem"
)

// bench is the bench command: a folder's benchmarks three ways, compared with
// benchstat, into the folder's BENCH_GENERATED.md.
func bench(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: structpages-dev bench folder")
		return 2
	}
	f := newFolder(args[0])
	if !hasBenchmarks(f) {
		fmt.Fprintf(os.Stderr, "%s has no benchmarks yet: add a BenchmarkE2E to its e2e_test.go, as examples/todo has\n",
			f.dir)
		return 2
	}
	onInterrupt(f.cleanGenerated)
	if err := benchFolder(f); err != nil {
		fmt.Fprintf(os.Stderr, "structpages-dev: %s: %v\n", f.dir, err)
		return 1
	}
	return 0
}

var benchmarkFunc = regexp.MustCompile(`(?m)^func Benchmark`)

// hasBenchmarks reports whether f's own package declares a benchmark.
func hasBenchmarks(f folder) bool {
	files, _ := filepath.Glob(filepath.Join(f.dir, "*_test.go"))
	for _, file := range files {
		if src, err := os.ReadFile(file); err == nil && benchmarkFunc.Match(src) {
			return true
		}
	}
	return false
}

func benchFolder(f folder) error {
	defer f.cleanGenerated()
	if f.usesGSX() {
		if err := gsxGenerate(f, false); err != nil {
			return err
		}
	}
	// In-process: each way's own test binary calls the code directly.
	inProcess := map[string]string{}
	for _, way := range ways {
		fmt.Printf("== %s, in-process: %s...\n", f.name, way)
		out, err := benchWay(f, way)
		if err != nil {
			return err
		}
		inProcess[way] = out
	}
	result, err := benchstat(inProcess)
	if err != nil {
		return err
	}
	fmt.Println(result)

	served, wasm := "(the library is no app: nothing to serve)", ""
	if f.name != "library" {
		served = "(not served: " + f.dir + " doesn't serve through the workers module yet)"
		if f.servesWorkers() {
			times := map[string]string{}
			for _, way := range ways {
				fmt.Printf("== %s, served: %s...\n", f.name, way)
				out, size, serr := benchServed(f, way)
				if serr != nil {
					return serr
				}
				times[way] = out
				if size > 0 {
					wasm = fmt.Sprintf("%d KB of", size/1024)
				}
			}
			if served, err = benchstat(times); err != nil {
				return err
			}
			fmt.Println(served)
		}
	}
	return writeBench(f, inProcess["reflect"], result, served, wasm)
}

// benchWay runs f's benchmarks one way, in-process.
func benchWay(f folder, way string) (string, error) {
	args := append([]string{"test"}, benchFlags...)
	args = append(args, benchmemArg)
	tool := "go"
	switch way {
	case "generated":
		if err := generate(f, false); err != nil {
			return "", err
		}
		args = append(args, "-tags", generatedTag)
	case "tinygo":
		if err := generate(f, true); err != nil {
			return "", err
		}
		tool = "tinygo"
	}
	out, err := run(f.dir, tool, append(args, ".")...)
	if err != nil {
		return "", fmt.Errorf("%s benchmarks, %s: %w\n%s", f.name, way, err, failureLines(out))
	}
	if way == "tinygo" {
		// TinyGo prints 0 for every allocs/op: left out.
		out = regexp.MustCompile(`\s+0 allocs/op`).ReplaceAllString(out, "")
	}
	return out, nil
}

// benchServed serves f one way and times its benchmarks against it over
// local HTTP, from one standard Go client (e2e.Bench with E2E_URL), so all
// three ways pay the same network cost. It returns them and, for TinyGo, the
// size of app.wasm.
func benchServed(f folder, way string) (times string, wasm int64, err error) {
	s, err := start(f, way, nil, true)
	if err != nil {
		return "", 0, err
	}
	defer s.stop()
	if werr := waitUp(s, 7*time.Minute); werr != nil {
		return "", 0, fmt.Errorf("%s, %s: %w", f.dir, way, werr)
	}
	args := append(append([]string{"test"}, benchFlags...), ".")
	cmd := exec.Command("go", args...) //nolint:gosec // the go toolchain, on the folder asked
	cmd.Dir = f.dir
	cmd.Env = append(os.Environ(), "E2E_URL=http://"+addr)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", 0, fmt.Errorf("%s, served %s: %w\n%s", f.dir, way, err, failureLines(string(out)))
	}
	return string(out), s.wasm, nil
}

// benchstat compares the three ways' benchmark output, as the benchstat
// command does (mise installs it), and returns its tables.
func benchstat(outs map[string]string) (string, error) {
	dir, err := os.MkdirTemp("", "structpages-dev-bench-")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	args := []string{"-ignore", "goos,goarch,pkg,cpu,/gomaxprocs"}
	for _, way := range ways {
		file := filepath.Join(dir, way)
		if werr := os.WriteFile(file, []byte(outs[way]), 0o600); werr != nil {
			return "", werr
		}
		args = append(args, way+"="+file)
	}
	out, err := exec.Command("benchstat", args...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("benchstat: %w\n%s", err, out)
	}
	return strings.TrimRight(string(out), "\n"), nil
}

// writeBench writes f's BENCH_GENERATED.md.
func writeBench(f folder, reflect, inProcess, served, wasm string) error {
	file := filepath.Join(f.dir, "BENCH_GENERATED.md")
	task := "bench"
	if f.name != "library" {
		task = "example:" + f.name + ":bench"
	}
	cpu := ""
	if m := regexp.MustCompile(`(?m)^cpu: (.*)$`).FindStringSubmatch(reflect); len(m) == 2 {
		cpu = ", " + m[1]
	}
	if wasm == "" {
		wasm = "no"
	}
	doc := fmt.Sprintf(`<!-- Generated by `+"`mise run %s`"+`. Do not edit: run it again. -->

# %s: benchmarks, three ways

- reflect: standard Go, structpages reading methods through reflect
- generated: standard Go, reading generated code instead (-tags structpages_generated)
- tinygo: TinyGo, which reads the generated code too

%s
%s, %s%s, workerd %s.
6 runs of 200ms each.

## In-process

Each way's test binary calls the code directly (TinyGo's natively, and it reports no allocs/op).

`+"```"+`
%s
`+"```"+`

## As served

Each way served on :8080 as its example:%s:serve:<way> task serves it (tinygo: %s WebAssembly in workerd,
what Cloudflare runs), timed over local HTTP by one standard Go client, so all three pay the same network cost.

`+"```"+`
%s
`+"```"+`
`, task, f.name, stamp(), version("go", "version"), version("tinygo", "version"), cpu,
		version("workerd", "--version"),
		inProcess, f.name, wasm, served)
	if err := os.WriteFile(file, []byte(doc), 0o600); err != nil {
		return err
	}
	fmt.Println("wrote", file)
	return nil
}

// stamp is the commit the numbers are from, and whether anything but the
// BENCH_GENERATED.md files was changed from it: writing them would count.
func stamp() string {
	commit, _ := run(".", "git", "rev-parse", "--short", "HEAD")
	s := "Commit " + strings.TrimSpace(commit)
	diff := exec.Command("git", "diff", "--quiet", "HEAD", "--", ".", ":(exclude,glob)**/BENCH_GENERATED.md")
	if err := diff.Run(); err != nil {
		s += " + uncommitted changes"
	}
	return s + ", " + time.Now().UTC().Format("2006-01-02T15:04Z") + "."
}

// version is the version a tool reports, as BENCH_GENERATED.md says it.
func version(tool, arg string) string {
	out, err := run(".", tool, arg)
	if err != nil {
		return "?"
	}
	fields := strings.Fields(out)
	switch tool {
	case "go": // go version go1.26.10 darwin/arm64
		if len(fields) >= 4 {
			return fields[2] + " " + fields[3]
		}
	case "tinygo": // tinygo version 0.43.0 darwin/arm64 (...)
		if len(fields) >= 4 {
			return fields[2] + " " + fields[3]
		}
	case "workerd": // workerd 2026-10-19
		if len(fields) >= 2 {
			return fields[1]
		}
	}
	return strings.TrimSpace(out)
}
