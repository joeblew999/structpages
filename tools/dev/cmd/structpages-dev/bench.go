package main

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/jackielii/structpages/e2e"
)

// benchFlags are the in-process benchmarks': 6 runs of 200ms each, with their
// allocations.
var benchFlags = []string{"-run", "NoTest", "-bench", ".", "-count", "6", "-benchtime", "200ms", "-benchmem"}

// The served measurement sends each recorded page for servedTime, servedRuns
// times, after sending it for servedWarm: half a second of requests is enough
// CPU that the operating system's 10ms steps are a few percent of it at worst.
// The start-up measurement starts the build startRuns times.
const (
	servedRuns = 6
	servedTime = 500 * time.Millisecond
	servedWarm = 250 * time.Millisecond
	startRuns  = 6
)

// variants are todo's other Worker builds: static-worker-plain with none of
// the three things the static-worker build has (the patched TinyGo, mise's
// tools:tinygo; the 8 MB starting heap; structpages-workers' glue), the rest
// with one each, so the report shows what each saves.
var variants = []string{"static-worker-plain", "static-worker-heap", "static-worker-gcpatch", "static-worker-glue"}

// bench is the bench command, one step: the current folder's benchmarks one
// way, into build/bench/ for report to compare. In-process, then, for an
// app, its build served: each recorded page timed, and its server's CPU, and
// the build started, timed to its first answer. A build names a variant of
// the way's build (static-worker-plain), timed served and started only.
func bench(args []string) int {
	if len(args) < 1 || len(args) > 2 || !slices.Contains(ways, args[0]) {
		fmt.Fprintln(os.Stderr, "usage: structpages-dev bench dynamic|static|static-worker [build]")
		return 2
	}
	way, build := args[0], args[0]
	if len(args) == 2 {
		build = args[1]
	}
	if err := benchOne(newFolder("."), way, build); err != nil {
		fmt.Fprintf(os.Stderr, "structpages-dev: %v\n", err)
		return 1
	}
	return 0
}

// benchOne is bench's one step.
func benchOne(f folder, way, build string) error {
	dir := filepath.Join(f.dir, "build", "bench")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	if build == way {
		fmt.Printf("%s, %s: in-process...\n", f.name, way)
		out, err := benchWay(f, way)
		if err != nil {
			return err
		}
		if werr := os.WriteFile(filepath.Join(dir, way+".txt"), []byte(out), 0o600); werr != nil {
			return werr
		}
	}
	if f.name == "library" {
		return nil
	}
	fmt.Printf("%s, %s: served...\n", f.name, build)
	served, err := timeServed(f, way, build)
	if err != nil {
		return err
	}
	if werr := os.WriteFile(filepath.Join(dir, build+"-served.txt"), []byte(served), 0o600); werr != nil {
		return werr
	}
	fmt.Printf("%s, %s: started...\n", f.name, build)
	started, err := timeStart(f, way, build)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, build+"-start.txt"), []byte(started), 0o600)
}

// benchWay runs f's benchmarks one way, in-process.
func benchWay(f folder, way string) (string, error) {
	args := append([]string{"test"}, benchFlags...)
	tool := "go"
	switch way {
	case "static":
		args = append(args, "-tags", staticTag)
	case "static-worker":
		tool = "tinygo"
	}
	out, err := run(f.dir, tool, append(args, ".")...)
	if err != nil {
		return "", fmt.Errorf("%s benchmarks, %s: %w\n%s", f.name, way, err, failureLines(out))
	}
	if way == "static-worker" {
		// TinyGo prints 0 for every allocs/op: left out.
		out = regexp.MustCompile(`\s+0 allocs/op`).ReplaceAllString(out, "")
	}
	return out, nil
}

// timeServed serves f's build and sends it every page its e2e test recorded
// (e2e.Reads, what e2e.Bench times in-process) from one standard Go client,
// the same for every way: for each page, servedRuns runs of servedTime, each
// a line in the go test -bench format benchstat reads, with the time per
// request and the server process's CPU per request.
func timeServed(f folder, way, build string) (string, error) {
	reads, err := e2e.Reads(filepath.Join(f.dir, "testdata", "e2e.generated.txt"))
	if err != nil {
		return "", err
	}
	s, err := startBuild(f, way, build, nil)
	if err != nil {
		return "", err
	}
	defer s.stop()
	if werr := waitUp(s, time.Minute); werr != nil {
		return "", fmt.Errorf("%s, %s: %w", f.dir, build, werr)
	}
	pid := s.cmd.Process.Pid
	client := &http.Client{Timeout: time.Minute}
	var out strings.Builder
	for _, r := range reads {
		get := func() error {
			req, rerr := http.NewRequest(http.MethodGet, "http://"+addr+r.Path, http.NoBody) //nolint:noctx // a local benchmark
			if rerr != nil {
				return rerr
			}
			req.Header = r.Header.Clone()
			resp, rerr := client.Do(req)
			if rerr != nil {
				return rerr
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			return resp.Body.Close()
		}
		for begin := time.Now(); time.Since(begin) < servedWarm; {
			if gerr := get(); gerr != nil {
				return "", gerr
			}
		}
		for range servedRuns {
			cpu0, cerr := cpuTime(pid)
			if cerr != nil {
				return "", cerr
			}
			n, begin := 0, time.Now()
			for time.Since(begin) < servedTime {
				if gerr := get(); gerr != nil {
					return "", gerr
				}
				n++
			}
			wall := time.Since(begin)
			cpu1, cerr := cpuTime(pid)
			if cerr != nil {
				return "", cerr
			}
			fmt.Fprintf(&out, "BenchmarkServed/%s\t%d\t%d ns/op\t%d cpu-ns/op\n",
				r.Name, n, wall.Nanoseconds()/int64(n), (cpu1-cpu0).Nanoseconds()/int64(n))
		}
	}
	return out.String(), nil
}

// timeStart starts f's build startRuns times, each timed from starting it to
// its first answer to GET /: in the go test -bench format benchstat reads.
// Not the CPU it spent: for a Go build, less than the operating system's
// 10ms steps.
func timeStart(f folder, way, build string) (string, error) {
	var out strings.Builder
	for range startRuns {
		begin := time.Now()
		s, err := startBuild(f, way, build, nil)
		if err != nil {
			return "", err
		}
		answered := false
		for !answered && time.Since(begin) < time.Minute {
			resp, gerr := http.Get("http://" + addr + "/") //nolint:noctx // a local benchmark
			if gerr == nil {
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
				answered = true
				break
			}
			time.Sleep(2 * time.Millisecond)
		}
		took := time.Since(begin)
		s.stop()
		if !answered {
			return "", fmt.Errorf("%s, %s: no answer a minute after starting", f.dir, build)
		}
		fmt.Fprintf(&out, "BenchmarkStart\t1\t%d ns/op\n", took.Nanoseconds())
	}
	return out.String(), nil
}

// report is the report command: the current folder's benchmark numbers, from
// its bench steps, compared with benchstat into its BENCH_GENERATED.md.
func report(args []string) int {
	if len(args) != 0 {
		fmt.Fprintln(os.Stderr, "usage: structpages-dev report")
		return 2
	}
	f := newFolder(".")
	dir := filepath.Join(f.dir, "build", "bench")
	// The ways the folder has numbers for: all three, or fewer for an
	// example that can't build one (html-template has no static-worker).
	var present []string
	for _, way := range ways {
		if fileExists(filepath.Join(dir, way+".txt")) {
			present = append(present, way)
		}
	}
	if len(present) == 0 {
		fail(fmt.Errorf("no benchmark numbers in %s: run its bench tasks first", dir))
	}
	r := benchReport{folder: f, present: present}
	var err error
	if r.inProcess, err = benchstat(dir, present, ".txt"); err != nil {
		fail(err)
	}
	if f.name != "library" {
		if r.served, err = benchstat(dir, present, "-served.txt"); err != nil {
			fail(err)
		}
		if r.started, err = benchstat(dir, present, "-start.txt"); err != nil {
			fail(err)
		}
		r.worker = workerBuild(filepath.Join(f.dir, "build", "static-worker"))
		var builds []string
		for _, v := range variants {
			if fileExists(filepath.Join(dir, v+"-served.txt")) {
				builds = append(builds, v)
			}
		}
		if len(builds) > 0 {
			builds = append(builds, "static-worker")
			if r.variantsServed, err = benchstat(dir, builds, "-served.txt"); err != nil {
				fail(err)
			}
			if r.variantsStarted, err = benchstat(dir, builds, "-start.txt"); err != nil {
				fail(err)
			}
		}
	}
	dynamic, _ := os.ReadFile(filepath.Join(dir, present[0]+".txt"))
	if err := r.write(string(dynamic)); err != nil {
		fail(err)
	}
	return 0
}

// benchstat compares the names' files in dir, each its name plus suffix, as
// the benchstat command does (mise installs it), the first the base the
// others are compared with, and returns its tables.
func benchstat(dir string, names []string, suffix string) (string, error) {
	args := []string{"-ignore", "goos,goarch,pkg,cpu,/gomaxprocs"}
	for _, name := range names {
		file := filepath.Join(dir, name+suffix)
		if !fileExists(file) {
			return "", fmt.Errorf("no %s: run bench %s first", file, name)
		}
		args = append(args, name+"="+file)
	}
	out, err := exec.Command("benchstat", args...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("benchstat: %w\n%s", err, out)
	}
	return strings.TrimRight(string(out), "\n"), nil
}

// workerBuild says what a Worker build is: its app.wasm's size, raw and
// gzipped, and how structpages-workers built it (its build.txt), or "" when
// there is none.
func workerBuild(dir string) string {
	wasm, err := os.ReadFile(filepath.Join(dir, "app.wasm"))
	if err != nil {
		return ""
	}
	var gz bytes.Buffer
	w, _ := gzip.NewWriterLevel(&gz, gzip.BestCompression)
	_, _ = w.Write(wasm)
	_ = w.Close()
	how, _ := os.ReadFile(filepath.Join(dir, "build.txt"))
	return fmt.Sprintf("static-worker's app.wasm is %d KB, %d KB gzipped: Cloudflare allows 3 MB gzipped on "+
		"Workers Free,\n"+
		"10 MB on Workers Paid. structpages-workers built it with %s.",
		len(wasm)/1024, gz.Len()/1024, strings.TrimSpace(string(how)))
}

// wayDescriptions say what each way, and each of todo's other Worker builds,
// is, for BENCH_GENERATED.md.
var wayDescriptions = map[string]string{
	"dynamic":               "standard Go, structpages finding the app's methods at run time, with reflect",
	"static":                "standard Go, methods known at compile time from generated code (-tags structpages_static)",
	"static-worker":         "a Cloudflare Worker, TinyGo WebAssembly, static too (all three, as every Worker build)",
	"static-worker-plain":   "the Worker as TinyGo and workers-go ship (-heap 0 -glue workers-go)",
	"static-worker-heap":    "with the 8 MB starting heap only (-glue workers-go)",
	"static-worker-gcpatch": "with the patched TinyGo only, mise's tools:tinygo (-heap 0 -glue workers-go -tinygoroot)",
	"static-worker-glue":    "with structpages-workers' glue only: one crossing, runtimes warmed and pooled (-heap 0)",
}

// benchReport is a folder's BENCH_GENERATED.md: benchstat's tables, and what
// they compare.
type benchReport struct {
	folder                             folder
	present                            []string
	inProcess, served, started, worker string
	variantsServed, variantsStarted    string
}

// write writes the report, dynamic the first way's in-process output, for
// the machine it ran on.
func (r *benchReport) write(dynamic string) error {
	f := r.folder
	task := "bench"
	if f.name != "library" {
		task = "example:" + f.name + ":bench"
	}
	cpu := ""
	if m := regexp.MustCompile(`(?m)^cpu: (.*)$`).FindStringSubmatch(dynamic); len(m) == 2 {
		cpu = ", " + m[1]
	}
	var b strings.Builder
	fence := func(s string) { b.WriteString("```\n" + s + "\n```\n") }
	fmt.Fprintf(&b, "<!-- Generated by `mise run %s`. Do not edit: run it again. -->\n\n", task)
	count := map[int]string{1: "one way", 2: "two ways", 3: "three ways"}[len(r.present)]
	fmt.Fprintf(&b, "# %s: benchmarks, %s\n\n", f.name, count)
	for _, way := range r.present {
		fmt.Fprintf(&b, "- %s: %s\n", way, wayDescriptions[way])
	}
	fmt.Fprintf(&b, "\n%s\n%s, %s%s, workerd %s.\n\n", stamp(), version("go", "version"), version("tinygo", "version"),
		cpu, version("workerd", "--version"))
	b.WriteString("## In-process\n\nEach way's test binary calls the code directly, 6 runs of 200ms each " +
		"(TinyGo's natively, and it reports no allocs/op).\n\n")
	fence(r.inProcess)
	if f.name == "library" {
		b.WriteString("\nThe library is no app: nothing to serve or start.\n")
		return r.save(b.String())
	}
	fmt.Fprintf(&b, "\n## As served\n\nEach way's build served on :8080 as its example:%s:serve:<way> task "+
		"serves it, and every "+
		"page its e2e test\nrecorded sent to it by one standard Go client for %v at a time, %d runs, after %v to "+
		"warm it: the time\na request takes (sec/op), and the server's CPU for it (cpu-sec/op), the operating system's "+
		"count of the\nserver process's CPU before and after, divided by the requests. CPU is what Cloudflare bills a "+
		"Worker for and\nlimits.\n\n", f.name, servedTime, servedRuns, servedWarm)
	fence(r.served)
	fmt.Fprintf(&b, "\n## Starting\n\nEach way's build started %d times: the time from starting it to its first answer "+
		"to GET /.\n\n", startRuns)
	fence(r.started)
	if r.worker != "" {
		b.WriteString("\n## The Worker\n\n" + r.worker + "\n")
	}
	if r.variantsServed != "" {
		b.WriteString("\nThe Worker built five ways, to show what the patched TinyGo, the starting heap and the glue " +
			"each save, served\nand started as above, compared with the first, which has none of them:\n\n")
		for _, v := range append(slices.Clone(variants), "static-worker") {
			fmt.Fprintf(&b, "- %s: %s\n", v, wayDescriptions[v])
		}
		b.WriteString("\n")
		fence(r.variantsServed + "\n\n" + r.variantsStarted)
	}
	return r.save(b.String())
}

// save writes doc as the folder's BENCH_GENERATED.md.
func (r *benchReport) save(doc string) error {
	file := filepath.Join(r.folder.dir, "BENCH_GENERATED.md")
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
	// The whole repository, wherever it runs from.
	diff := exec.Command("git", "-C", root, "diff", "--quiet", "HEAD", "--", //nolint:gosec // git, on this repository
		".", ":(exclude,glob)**/BENCH_GENERATED.md")
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
