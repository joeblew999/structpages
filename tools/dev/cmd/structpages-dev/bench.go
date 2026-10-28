package main

import (
	"bytes"
	"cmp"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
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

// variants are todo's other Worker builds: the static-worker build, which
// has every one of structpages-workers' defaults, with one turned off each,
// so the report shows what each saves, and static-worker-plain with none.
var variants = []string{
	"static-worker-no-patch", "static-worker-no-heap", "static-worker-no-glue",
	"static-worker-no-opt", "static-worker-no-leaking", "static-worker-plain",
}

// bench is the bench command, one step: the current folder's benchmarks one
// way, into build/bench/ for report to compare. In-process, then, for an
// app, its build served: each recorded page timed, and its server's CPU, and
// the build started, timed to its first answer. A build names a variant of
// the way's build (static-worker-plain), timed served and started only.
func bench(args []string) int {
	if len(args) == 1 && args[0] == "floor" {
		if err := benchFloors(newFolder(".")); err != nil {
			fmt.Fprintf(os.Stderr, "structpages-dev: %v\n", err)
			return 1
		}
		return 0
	}
	if len(args) < 1 || len(args) > 2 || !slices.Contains(ways, args[0]) {
		fmt.Fprintln(os.Stderr, "usage: structpages-dev bench dynamic|static|static-worker [build]\n"+
			"       structpages-dev bench floor")
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
		// The Worker's code is built for speed, as structpages-workers builds it.
		tool, args = "tinygo", append(args, "-opt=2")
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

// benchFloors is bench floor, one step: each floor served and timed as
// timeServed times a build, into build/bench/.
func benchFloors(f folder) error {
	if f.name == "library" {
		return errors.New("the library is no app: it has no recorded pages to serve")
	}
	reads, err := e2e.Reads(filepath.Join(f.dir, "testdata", "e2e.generated.txt"))
	if err != nil {
		return err
	}
	dir := filepath.Join(f.dir, "build", "bench")
	if merr := os.MkdirAll(dir, 0o750); merr != nil {
		return merr
	}
	for _, name := range floors {
		fmt.Printf("%s, %s: served...\n", f.name, name)
		s, serr := startFloor(f, name, reads)
		if serr != nil {
			return serr
		}
		served, terr := timeServer(f, name, s, reads)
		s.stop()
		if terr != nil {
			return terr
		}
		if werr := os.WriteFile(filepath.Join(dir, name+"-served.txt"), []byte(served), 0o600); werr != nil {
			return werr
		}
	}
	return nil
}

// timeServed serves f's build and sends it every page its e2e test recorded
// (e2e.Reads, what e2e.Bench times in-process): timeServer.
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
	return timeServer(f, build, s, reads)
}

// timeServer sends s, serving name, every page in reads from one standard Go
// client, the same for every way and floor: for each page, servedRuns runs
// of servedTime, each a line in the go test -bench format benchstat reads,
// with the time per request and the server process's CPU per request.
func timeServer(f folder, name string, s *server, reads []e2e.Read) (string, error) {
	if werr := waitUp(s, time.Minute); werr != nil {
		return "", fmt.Errorf("%s, %s: %w", f.dir, name, werr)
	}
	pid := s.cmd.Process.Pid
	client := &http.Client{Timeout: time.Minute}
	var out strings.Builder
	for i := range reads {
		r := &reads[i]
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
	r := benchReport{folder: f, dir: dir, present: present}
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
		if fileExists(filepath.Join(dir, floors[0]+"-served.txt")) {
			if r.floors, err = benchstat(dir, floors, "-served.txt"); err != nil {
				fail(err)
			}
		}
		r.worker = workerBuild(filepath.Join(f.dir, "build", "static-worker"))
		builds := []string{"static-worker"}
		for _, v := range variants {
			if fileExists(filepath.Join(dir, v+"-served.txt")) {
				builds = append(builds, v)
			}
		}
		if len(builds) > 1 {
			r.variants = builds
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

// benchReport is a folder's BENCH_GENERATED.md: benchstat's tables, and what
// they compare.
type benchReport struct {
	folder                                     folder
	dir                                        string
	present, variants                          []string
	inProcess, served, started, floors, worker string
	variantsServed, variantsStarted            string
}

// servedCPU is a served file's CPU per request for each page, the median of
// its runs, in µs, and its pages in order.
func servedCPU(file string) (medians map[string]float64, pages []string, err error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, nil, err
	}
	runs := map[string][]float64{}
	line := regexp.MustCompile(`^BenchmarkServed/(\S+)\s+\d+\s+\d+ ns/op\s+(\d+) cpu-ns/op`)
	for _, l := range strings.Split(string(data), "\n") {
		m := line.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		if _, seen := runs[m[1]]; !seen {
			pages = append(pages, m[1])
		}
		ns, _ := strconv.ParseFloat(m[2], 64)
		runs[m[1]] = append(runs[m[1]], ns/1e3)
	}
	medians = map[string]float64{}
	for page, rs := range runs {
		slices.Sort(rs)
		medians[page] = rs[len(rs)/2]
		if len(rs)%2 == 0 {
			medians[page] = (rs[len(rs)/2-1] + rs[len(rs)/2]) / 2
		}
	}
	return medians, pages, nil
}

// inShort is the report's first table: for each page, each way's build
// served, its server with no app, and the difference, the way's own CPU per
// request; or "" without the floors' numbers.
func (r *benchReport) inShort() string {
	cpu := map[string]map[string]float64{}
	var pages []string
	for _, name := range append(slices.Clone(r.present), floors...) {
		m, p, err := servedCPU(filepath.Join(r.dir, name+"-served.txt"))
		if err != nil {
			return ""
		}
		cpu[name] = m
		if pages == nil {
			pages = p
		}
	}
	// Each page as its recording names it.
	labels := map[string]string{}
	if reads, err := e2e.Reads(filepath.Join(r.folder.dir, "testdata", "e2e.generated.txt")); err == nil {
		for i := range reads {
			read := &reads[i]
			labels[read.Name] = "GET " + read.Path
			if read.Header.Get("HX-Request") != "" {
				labels[read.Name] += " (htmx)"
			}
		}
	}
	var b strings.Builder
	b.WriteString("| page | CPU per request |")
	for _, way := range r.present {
		b.WriteString(" " + way + " |")
	}
	b.WriteString("\n|---|---|" + strings.Repeat("---|", len(r.present)) + "\n")
	us := func(v float64) string { return fmt.Sprintf(" %.1f µs |", v) }
	for _, page := range pages {
		rows := []struct {
			label string
			value func(way string) float64
		}{
			{"the build, served", func(way string) float64 { return cpu[way][page] }},
			{"its server alone, no app", func(way string) float64 { return cpu[floorOf(way)][page] }},
			{"**the way's own**", func(way string) float64 { return cpu[way][page] - cpu[floorOf(way)][page] }},
		}
		for i, row := range rows {
			name := ""
			if i == 0 {
				name = "`" + cmp.Or(labels[page], page) + "`"
			}
			fmt.Fprintf(&b, "| %s | %s |", name, row.label)
			for _, way := range r.present {
				b.WriteString(us(row.value(way)))
			}
			b.WriteString("\n")
		}
	}
	return b.String()
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
		"10 MB on Workers Paid. structpages-workers built it with its defaults: %s.",
		len(wasm)/1024, gz.Len()/1024, strings.TrimSpace(string(how)))
}

// wayDescriptions say what each way, floor and Worker build is, for
// BENCH_GENERATED.md.
var wayDescriptions = map[string]string{
	"dynamic": "standard Go, structpages finding the app's methods at run time, with reflect",
	"static":  "standard Go, methods known at compile time from generated code (-tags structpages_static)",
	"static-worker": "a Cloudflare Worker: TinyGo WebAssembly, static too, built with every one of " +
		"structpages-workers' defaults",
	"floor-go": "Go's net/http, as the dynamic and static builds serve, answering the recorded pages with no app",
	"floor-workerd": "workerd, as the static-worker build is served, running a JavaScript Worker answering the " +
		"recorded pages",
	"static-worker-no-patch": "the TinyGo installed, not mise's tools:tinygo with its runtime patched " +
		"(-tinygoroot installed)",
	"static-worker-no-heap": "TinyGo's own starting heap, not 8 MB (-heap 0)",
	"static-worker-no-glue": "workers-go's glue, one runtime kept for good, each request through workers-go's " +
		"request path; so TinyGo's collector too (-glue workers-go -gc precise)",
	"static-worker-no-opt":     "TinyGo's smallest code, not its fastest (-opt z)",
	"static-worker-no-leaking": "TinyGo's own collector, not the leaking one (-gc precise)",
	"static-worker-plain": "none of them, the Worker as TinyGo and workers-go ship " +
		"(-tinygoroot installed -heap 0 -glue workers-go -opt z -gc precise)",
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
	if f.name == "library" {
		b.WriteString("## In-process\n\nEach way's test binary calls the code directly, 6 runs of 200ms each " +
			"(TinyGo's natively, and it reports no allocs/op).\n\n")
		fence(r.inProcess)
		b.WriteString("\nThe library is no app: nothing to serve or start.\n")
		return r.save(b.String())
	}
	if short := r.inShort(); short != "" {
		b.WriteString("## In short\n\nThe server's CPU for one request, the median of 6 runs (As served, below). " +
			"Each way runs on a\nserver of its own: Go's net/http for dynamic and static, workerd for " +
			"static-worker. Each server's CPU\nanswering the same page with no app behind it is below the build's " +
			"(The servers alone, below), and\nthe difference is the way's own: what structpages and the app cost on " +
			"top of the server. On Cloudflare,\nworkerd's own work is Cloudflare's: a Worker is billed for its own " +
			"CPU.\n\n")
		b.WriteString(short)
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "## As served\n\nEach way's build served on :8080 as its example:%s:serve:<way> task "+
		"serves it, and every "+
		"page its e2e test\nrecorded sent to it by one standard Go client for %v at a time, %d runs, after %v to "+
		"warm it: the time\na request takes (sec/op), and the server's CPU for it (cpu-sec/op), the operating system's "+
		"count of the\nserver process's CPU before and after, divided by the requests.\n\n",
		f.name, servedTime, servedRuns, servedWarm)
	fence(r.served)
	if r.floors != "" {
		b.WriteString("\n## The servers alone\n\nThe two servers, each answering the recorded pages with no app " +
			"behind them (example:" + f.name + ":bench:floor),\nserved and timed as above:\n\n")
		for _, name := range floors {
			fmt.Fprintf(&b, "- %s: %s\n", name, wayDescriptions[name])
		}
		b.WriteString("\n")
		fence(r.floors)
	}
	b.WriteString("\n## In-process\n\nEach way's test binary calls the code directly, 6 runs of 200ms each " +
		"(TinyGo's natively at -opt=2, not\nas WebAssembly, with its native collector; it reports no allocs/op).\n\n")
	fence(r.inProcess)
	fmt.Fprintf(&b, "\n## Starting\n\nEach way's build started %d times: the time from starting it to its first answer "+
		"to GET /,\nworkerd's own start included.\n\n", startRuns)
	fence(r.started)
	if r.worker != "" {
		b.WriteString("\n## The Worker\n\n" + r.worker + "\n")
	}
	if r.variantsServed != "" {
		b.WriteString("\nThe Worker built with one of structpages-workers' defaults turned off each, and with none, to " +
			"show what\neach saves, served and started as above, compared with static-worker, which has them all:\n\n")
		for _, v := range r.variants {
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
