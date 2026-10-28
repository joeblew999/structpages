package main

import (
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
)

// test is the test command, one step: the current folder's tests one way,
// then a line with how many passed, what was skipped and why, and what
// failed. It returns the exit code.
func test(args []string) int {
	if len(args) != 1 || !slices.Contains(ways, args[0]) {
		fmt.Fprintln(os.Stderr, "usage: structpages-dev test dynamic|static|static-worker")
		return 2
	}
	if !testFolder(newFolder("."), args) {
		return 1
	}
	return 0
}

// result is one way's outcome for one folder.
type result struct {
	way           string
	built, tested bool // built: every package built; tested: the tests ran and passed
	passed        int
	failed        int
	skips         []string // "Name: why"
	report        string   // the first lines of a failure
}

func (r *result) cell() string {
	switch {
	case !r.built:
		return r.way + ": BUILD FAILED"
	case r.passed == 0 && r.failed == 0 && len(r.skips) == 0 && r.tested:
		return r.way + ": builds, no tests"
	}
	s := fmt.Sprintf("%s: %s, %d passed", r.way, map[bool]string{true: "pass", false: "FAIL"}[r.tested], r.passed)
	if r.failed > 0 {
		s += fmt.Sprintf(", %d failed", r.failed)
	}
	if len(r.skips) > 0 {
		s += fmt.Sprintf(", %d skipped", len(r.skips))
	}
	return s
}

// testFolder checks f each way and prints its line; it reports whether all
// passed.
func testFolder(f folder, chosen []string) bool {
	ok := true
	var reports []string
	tests := f.hasTests()
	results := make([]result, 0, len(chosen))
	for _, way := range chosen {
		r := testWay(f, way, tests)
		results = append(results, r)
		if !r.built || !r.tested {
			ok = false
		}
		if r.report != "" {
			reports = append(reports, fmt.Sprintf("== %s, %s:\n%s", f.name, r.way, r.report))
		}
	}
	line := fmt.Sprintf("%-20s", f.name)
	for _, r := range results {
		line += fmt.Sprintf(" %-41s", r.cell())
	}
	fmt.Println(strings.TrimRight(line, " "))
	printSkips(results)
	for _, r := range reports {
		fmt.Println()
		fmt.Println(r)
	}
	return ok
}

// printSkips lists each way's skipped tests, the static and static-worker ways'
// once when they are the same.
func printSkips(results []result) {
	by := map[string][]string{}
	for _, r := range results {
		by[r.way] = r.skips
	}
	if len(by["static"]) > 0 && slices.Equal(by["static"], by["static-worker"]) {
		fmt.Println("  skipped in static and static-worker:")
		for _, s := range by["static"] {
			fmt.Println("  " + s)
		}
		delete(by, "static")
		delete(by, "static-worker")
	}
	for _, way := range ways {
		if len(by[way]) > 0 {
			fmt.Printf("  skipped in %s:\n", way)
			for _, s := range by[way] {
				fmt.Println("  " + s)
			}
		}
	}
}

// testWay builds every package of f one way, then runs its tests if it has
// any.
func testWay(f folder, way string, tests bool) result {
	r := result{way: way}
	var build, testCmd []string
	switch way {
	case "dynamic":
		build = []string{"go", "build", "-o", os.DevNull, "./..."}
		testCmd = []string{"go", "test", "-race", "-count=1", "-v", "./..."}
	case "static":
		build = []string{"go", "build", "-tags", staticTag, "-o", os.DevNull, "./..."}
		testCmd = []string{"go", "test", "-race", "-count=1", "-tags", staticTag, "-v", "./..."}
	case "static-worker":
		// A library has no main package for TinyGo to build; its tests build it.
		if f.name != "library" {
			build = []string{"tinygo", "build", "-o", os.DevNull, "."}
		}
		testCmd = []string{"tinygo", "test", "-v", "./..."}
	}
	if build != nil {
		if out, err := run(f.dir, build[0], build[1:]...); err != nil {
			r.report = firstLines(out, 20)
			return r
		}
	}
	r.built = true
	if !tests {
		r.tested = true
		return r
	}
	out, err := run(f.dir, testCmd[0], testCmd[1:]...)
	r.passed, r.failed, r.skips = readVerbose(out)
	r.tested = err == nil
	if err != nil {
		r.report = failureLines(out)
	}
	return r
}

var (
	resultLine = regexp.MustCompile(`^\s*--- (PASS|FAIL|SKIP): (\S+)`)
	fileLine   = regexp.MustCompile(`^\S+\.go:\d+: `)
)

// readVerbose counts what go test -v (or tinygo test -v) printed: tests
// passed and failed, and each skipped test with why, its first log line,
// printed after its "=== RUN" (Go puts file:line first; TinyGo doesn't).
func readVerbose(out string) (passed, failed int, skips []string) {
	cur := ""
	why := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if name, ok := strings.CutPrefix(line, "=== RUN"); ok {
			cur = strings.TrimSpace(name)
			continue
		}
		if m := resultLine.FindStringSubmatch(line); m != nil {
			switch m[1] {
			case "PASS":
				passed++
			case "FAIL":
				failed++
			case "SKIP":
				skips = append(skips, m[2]+": "+why[m[2]])
			}
			continue
		}
		trimmed := strings.TrimSpace(line)
		if _, seen := why[cur]; cur == "" || seen || trimmed == "" || strings.HasPrefix(trimmed, "===") ||
			strings.HasPrefix(line, "PASS") || strings.HasPrefix(line, "FAIL") || strings.HasPrefix(line, "ok") {
			continue
		}
		why[cur] = fileLine.ReplaceAllString(trimmed, "")
	}
	return passed, failed, skips
}

// failureLines is what each failed test printed, from its "=== RUN" to its
// "--- FAIL" (a test's log lines come before its result), and any panic;
// at most 40 lines.
func failureLines(out string) string {
	lines := strings.Split(out, "\n")
	started := map[string]int{}
	var kept []string
	for i, line := range lines {
		if name, ok := strings.CutPrefix(line, "=== RUN"); ok {
			started[strings.TrimSpace(name)] = i
			continue
		}
		if m := resultLine.FindStringSubmatch(line); len(m) == 3 && m[1] == "FAIL" {
			if !strings.Contains(m[2], "/") || len(kept) == 0 { // a subtest's lines are its parent's too
				kept = append(kept, lines[started[m[2]]:i+1]...)
			}
		}
		if strings.HasPrefix(line, "panic") {
			kept = append(kept, lines[i:min(i+12, len(lines))]...)
		}
	}
	if len(kept) == 0 {
		return firstLines(out, 20)
	}
	return strings.Join(kept[:min(40, len(kept))], "\n")
}

func firstLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.Join(lines[:min(n, len(lines))], "\n")
}
