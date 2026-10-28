// Command structpages-workers builds a structpages app as a Cloudflare
// Worker: TinyGo WebAssembly, with the JavaScript glue and workerd config it
// runs with. First make a TinyGo with its runtime patched, once; then, in
// the app's folder, build with it and serve what it built:
//
//	go tool structpages-workers tinygo -o build/tinygo
//	go tool structpages-workers [-o dir] [-tinygoroot build/tinygo]
//	workerd serve build/config.capnp
//
// Every default is the fastest Worker structpages knows how to build; each
// flag turns one thing off, to measure what it saves (examples/todo's
// static-worker-no-* builds, its BENCH_GENERATED.md):
//
//   - -tinygoroot: the TinyGo to build with, default build/tinygo, as the
//     tinygo command makes it ("installed": TinyGo as it ships). See patches.
//   - -heap: the WebAssembly's starting heap, in MB (default 8; 0: TinyGo's
//     own), room for a runtime's requests without growing its memory.
//   - -opt: TinyGo's -opt (default 2, the fastest code; z: the smallest).
//   - -gc: TinyGo's -gc (default leaking: memory is never collected, and the
//     glue gives a runtime no more requests once its heap is used up, so no
//     request ever waits for a collection; precise: TinyGo's own).
//   - -stack: TinyGo's -stack-size (default 64kb, TinyGo's own).
//   - -glue: the JavaScript glue (default structpages: worker.mjs; workers-go:
//     the glue worker.mjs replaced, with the collector it then needs).
//   - -warm: the paths runtimes started while the module loads answer a GET
//     of first (comma-separated, default /; none with -warm "").
//
// tinygo makes a copy of the TinyGo installed (tinygo env TINYGOROOT) with
// patches applied (each says what it changes and why). Its files are linked
// to the install's (a clone on macOS, hard links elsewhere) and the patched
// ones written anew, so the install is never changed.
//
// The build writes into dir (default build), in this order: workers-go's
// glue, wasm_exec.js and runtime.mjs (workers-assets-gen, at the version the
// app's go.mod requires); its own worker.mjs over the one workers-go wrote,
// and config.capnp, serving on :8080; then app.wasm (tinygo build -target
// wasm -no-debug, with the flags above); then build.txt, which says how it
// was built.
//
// worker.mjs keeps Go runtimes between requests, a pool of them, starts some
// while the module loads, warmed, and gives a request to Go and takes its
// answer back as numbers, the bytes in Go's memory: worker.mjs says how, and
// the workers module's Serve is its Go side.
package main

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
)

//go:embed worker.mjs worker-workers-go.mjs config.capnp
var glue embed.FS

// warmLine is the line of worker.mjs that -warm's paths replace.
const warmLine = `const warmPaths = ["/"]; // structpages-workers -warm`

// A change the tinygo command makes to TinyGo's runtime source, read at
// build time (the compiler is not rebuilt): old replaced by new, or, when old
// is "", file written with new.
type patch struct{ file, old, new string }

var patches = []patch{
	// No collection at every pause. TinyGo runs a full collection whenever
	// its scheduler goes idle once 32 finalizers were registered since the
	// last one, and every JavaScript value Go holds registers one: a Worker
	// goes idle at every await, so it collected on every request. The
	// collector still runs when the heap is full. Upstream:
	// tinygo-org/tinygo#5800 (when fixed: drop the patch).
	{"src/runtime/gc_finalizer.go", "const finalizerGCThreshold = 32", "const finalizerGCThreshold = 0"},
	// Goroutine stacks reused. A goroutine's stack is allocated, and
	// cleared, when it starts, and every call into Go from JavaScript
	// starts one: workers.Serve's structpages_serve, once a request. With
	// these, a finished goroutine's stack is kept for the next one
	// (spareStacks, below), and under the leaking collector, which frees
	// nothing, no stack is lost per request. An exported function's
	// goroutine never exits, it is left paused once it returns, so it too
	// marks its stack finished, as TinyGo already does when finalizers run.
	{"src/internal/task/task_asyncify.go", `
	stack := runtime_alloc(stackSize, gclayout.Conservative.AsPtr())
`, `
	stack := spareStack(stackSize)
	if stack == nil {
		stack = runtime_alloc(stackSize, gclayout.Conservative.AsPtr())
	}
`},
	{"src/internal/task/task_asyncify.go", `
		runtime_freeTaskStack(t.state.stackBase)
`, `
		if !keepStack(&t.state) {
			runtime_freeTaskStack(t.state.stackBase)
		}
`},
	{"src/internal/task/task_asyncify.go", `
		t.clearStack()
`, `
		if !keepStack(&t.state) {
			t.clearStack()
		}
`},
	{"src/runtime/scheduler_cooperative.go", `
	if finalizerIdleGC != nil {
		task.MarkFinishing()
	}
`, `
	task.MarkFinishing()
`},
	{"src/internal/task/task_asyncify_spare.go", "", spareStacks},
	// sync.Once's Do returns at once when its function has run, as Go's
	// does, without the lock and the deferred unlock TinyGo's takes on
	// every call: gsx's HTML escaping calls one for each string it writes.
	// done is set when the function has returned, as in Go, so that a Do
	// returning early never returns before it has. These builds run one
	// goroutine at a time (WebAssembly), so done needs no atomic.
	{"src/sync/once.go", `
func (o *Once) Do(f func()) {
	o.m.Lock()
	defer o.m.Unlock()
	if o.done {
		return
	}
	o.done = true
	f()
}
`, `
func (o *Once) Do(f func()) {
	if o.done {
		return
	}
	o.m.Lock()
	defer o.m.Unlock()
	if o.done {
		return
	}
	defer func() { o.done = true }()
	f()
}
`},
}

// spareStacks is the file the stack patch adds to TinyGo's internal/task.
const spareStacks = `//go:build scheduler.asyncify

// Added by structpages-workers tinygo: see its patches.

package task

import "unsafe"

// spareStacks are finished goroutines' stacks, all of one size, kept for
// new goroutines.
var spareStacks struct {
	stacks [4]unsafe.Pointer
	n      int
	size   uintptr
}

// spareStack takes a kept stack of size bytes, or is nil.
func spareStack(size uintptr) unsafe.Pointer {
	if spareStacks.n == 0 || spareStacks.size != size {
		return nil
	}
	spareStacks.n--
	stack := spareStacks.stacks[spareStacks.n]
	spareStacks.stacks[spareStacks.n] = nil
	return stack
}

// keepStack keeps s's stack, of a goroutine that will not run again, for
// the next one, and says whether it did.
func keepStack(s *state) bool {
	size := uintptr(s.top) - s.stackBase
	if spareStacks.n == len(spareStacks.stacks) || (spareStacks.n > 0 && spareStacks.size != size) {
		return false
	}
	spareStacks.stacks[spareStacks.n] = unsafe.Pointer(s.canaryPtr)
	spareStacks.n++
	spareStacks.size = size
	return true
}
`

func main() {
	if len(os.Args) > 1 && os.Args[1] == "tinygo" {
		flags := flag.NewFlagSet("tinygo", flag.ExitOnError)
		out := flags.String("o", "build/tinygo", "the directory to make the patched TinyGo in")
		_ = flags.Parse(os.Args[2:])
		if err := patchTinyGo(*out); err != nil {
			fmt.Fprintln(os.Stderr, "structpages-workers tinygo:", err)
			os.Exit(1)
		}
		return
	}
	out := flag.String("o", "build", "the directory to build the Worker into")
	heap := flag.Int("heap", 8, "the WebAssembly's starting heap, in MB (0: TinyGo's own)")
	root := flag.String("tinygoroot", "build/tinygo",
		`the TinyGo to build with, as structpages-workers tinygo makes it ("installed": TinyGo as it ships)`)
	warm := flag.String("warm", "/", "the paths runtimes started while the module loads warm with, comma-separated")
	glueName := flag.String("glue", "structpages",
		"the Worker's glue: structpages, or workers-go to measure what it saves")
	opt := flag.String("opt", "2", "TinyGo's -opt: 2 the fastest code, z the smallest")
	gc := flag.String("gc", "leaking",
		"TinyGo's -gc: leaking never collects (structpages' glue replaces a full runtime), precise is TinyGo's own")
	stack := flag.String("stack", "64kb", "TinyGo's -stack-size, per goroutine")
	flag.Parse()
	if *glueName != "structpages" && *glueName != "workers-go" {
		fmt.Fprintln(os.Stderr, "structpages-workers: -glue is structpages or workers-go")
		os.Exit(2)
	}
	if *glueName == "workers-go" && *gc == "leaking" {
		fmt.Fprintln(os.Stderr, "structpages-workers: workers-go's glue keeps one runtime for good, "+
			"so it needs a collector: -gc precise (structpages' glue replaces a runtime whose heap is full)")
		os.Exit(2)
	}
	if *root == "installed" {
		*root = ""
	}
	var paths []string
	if *warm != "" {
		paths = strings.Split(*warm, ",")
	}
	if err := build(*out, *heap, *root, *glueName, paths, tinygoFlags{*opt, *gc, *stack}); err != nil {
		fmt.Fprintln(os.Stderr, "structpages-workers:", err)
		os.Exit(1)
	}
}

// tinygoFlags are what the build passes TinyGo's -opt, -gc and -stack-size.
type tinygoFlags struct{ opt, gc, stack string }

// build builds the app in the working directory as a Worker into dir, with
// the TinyGo at root, or the one installed when root is "", and glueName's
// glue, its runtimes warmed with warm.
func build(dir string, heapMB int, root, glueName string, warm []string, tf tinygoFlags) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	const assetsGen = "github.com/syumai/workers-go/cmd/workers-assets-gen"
	if err := run(nil, "go", "run", assetsGen, "-mode", "tinygo", "-o", dir); err != nil {
		return err
	}
	if err := write(dir, glueName, warm); err != nil {
		return err
	}
	target := "wasm"
	if heapMB > 0 {
		target = filepath.Join(dir, "tinygo-target.json")
		spec := fmt.Sprintf(`{"inherits": ["wasm"], "ldflags": ["--initial-memory=%d"]}`+"\n", heapMB<<20)
		if err := os.WriteFile(target, []byte(spec), 0o600); err != nil {
			return err
		}
	}
	var env []string
	tinygo := "installed"
	if root != "" {
		abs, err := filepath.Abs(root)
		if err != nil {
			return err
		}
		if _, serr := os.Stat(filepath.Join(abs, "src", "runtime")); serr != nil {
			return fmt.Errorf("%s is no TinyGo: make it with structpages-workers tinygo -o %s "+
				"(or build with -tinygoroot installed, TinyGo as it ships)", root, root)
		}
		env, tinygo = []string{"TINYGOROOT=" + abs}, root
	}
	wasm := filepath.Join(dir, "app.wasm")
	if err := run(env, "tinygo", "build", "-o", wasm, "-target", target, "-no-debug",
		"-opt="+tf.opt, "-gc="+tf.gc, "-stack-size="+tf.stack, "."); err != nil {
		return err
	}
	heap := fmt.Sprintf("%dMB", heapMB)
	if heapMB == 0 {
		heap = "tinygo's own"
	}
	how := fmt.Sprintf("heap=%s tinygo=%s glue=%s warm=%s opt=%s gc=%s stack=%s\n",
		heap, tinygo, glueName, strings.Join(warm, ","), tf.opt, tf.gc, tf.stack)
	return os.WriteFile(filepath.Join(dir, "build.txt"), []byte(how), 0o600)
}

// write writes this command's worker.mjs, glueName's, and config.capnp into
// dir: worker.mjs with warm as the paths its runtimes warm with.
func write(dir, glueName string, warm []string) error {
	source := "worker.mjs"
	if glueName == "workers-go" {
		source = "worker-workers-go.mjs"
	}
	worker, err := glue.ReadFile(source)
	if err != nil {
		return err
	}
	if glueName == "structpages" {
		quoted := make([]string, len(warm))
		for i, p := range warm {
			quoted[i] = strconv.Quote(p)
		}
		line := "const warmPaths = [" + strings.Join(quoted, ", ") + "]; // structpages-workers -warm"
		if !bytes.Contains(worker, []byte(warmLine)) {
			return errors.New("worker.mjs has no warm paths line to set")
		}
		worker = bytes.Replace(worker, []byte(warmLine), []byte(line), 1)
	}
	if werr := os.WriteFile(filepath.Join(dir, "worker.mjs"), worker, 0o600); werr != nil {
		return werr
	}
	config, err := glue.ReadFile("config.capnp")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "config.capnp"), config, 0o600)
}

// patchTinyGo makes dir a copy of the TinyGo installed with patches applied,
// unless it is one already. Its .patched file names the TinyGo and the
// patches it was made with, and its go.mod keeps the copy out of whichever
// module dir is in: go test ./... there would read TinyGo's own sources.
func patchTinyGo(dir string) error {
	installed, err := output("tinygo", "env", "TINYGOROOT")
	if err != nil {
		return err
	}
	version, err := output("tinygo", "version")
	if err != nil {
		return err
	}
	sum := sha256.New()
	for _, p := range patches {
		fmt.Fprintf(sum, "%s\x00%s\x00%s\x00", p.file, p.old, p.new)
	}
	made := fmt.Sprintf("%s\npatches %x\n", version, sum.Sum(nil)[:8])
	marker := filepath.Join(dir, ".patched")
	if was, merr := os.ReadFile(marker); merr == nil && string(was) == made {
		fmt.Printf("%s is %s, patched already\n", dir, version)
		return nil
	}
	if rerr := os.RemoveAll(dir); rerr != nil {
		return rerr
	}
	if merr := os.MkdirAll(filepath.Dir(dir), 0o750); merr != nil {
		return merr
	}
	link := "-al"
	if runtime.GOOS == "darwin" {
		link = "-c"
	}
	if lerr := exec.Command("cp", link, "-R", installed, dir).Run(); lerr != nil { //nolint:gosec // TinyGo's own root
		if cerr := exec.Command("cp", "-R", installed, dir).Run(); cerr != nil { //nolint:gosec // TinyGo's own root
			return fmt.Errorf("copying TinyGo's root: %w", cerr)
		}
	}
	files, err := applyPatches(dir)
	if err != nil {
		return err
	}
	goMod := "// Written by structpages-workers tinygo: TinyGo's sources are no package of this module.\nmodule tinygo\n"
	if werr := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(goMod), 0o600); werr != nil {
		return werr
	}
	fmt.Printf("%s: %s, with %d patches to %s\n", dir, version, len(patches), strings.Join(files, ", "))
	return os.WriteFile(marker, []byte(made), 0o600)
}

// applyPatches applies patches to the TinyGo copy in dir, each file once,
// with all of its patches, and returns the files it wrote.
func applyPatches(dir string) ([]string, error) {
	var files []string
	for _, p := range patches {
		if !slices.Contains(files, p.file) {
			files = append(files, p.file)
		}
	}
	for _, name := range files {
		file := filepath.Join(dir, name)
		var src []byte
		for _, p := range patches {
			if p.file != name {
				continue
			}
			if p.old == "" {
				src = []byte(p.new)
				continue
			}
			if src == nil {
				var err error
				if src, err = os.ReadFile(file); err != nil {
					return nil, err
				}
			}
			if bytes.Count(src, []byte(p.old)) != 1 {
				return nil, fmt.Errorf("this TinyGo's %s has no single %q to patch: build with -tinygoroot installed", name, p.old)
			}
			src = bytes.Replace(src, []byte(p.old), []byte(p.new), 1)
		}
		// A new file, not the linked one written through: the install keeps its own.
		if rerr := os.Remove(file); rerr != nil && !os.IsNotExist(rerr) {
			return nil, rerr
		}
		if werr := os.WriteFile(file, src, 0o600); werr != nil { //nolint:gosec // a file of the copy's own
			return nil, werr
		}
	}
	return files, nil
}

// run runs a step of the build with env added, its output to this command's.
func run(env []string, name string, args ...string) error {
	cmd := exec.Command(name, args...) //nolint:gosec // the build's own steps
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %w", name, args[0], err)
	}
	return nil
}

// output is a command's output, its last line trimmed.
func output(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).Output()
	if err != nil {
		return "", fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	return strings.TrimSpace(lines[len(lines)-1]), nil
}
