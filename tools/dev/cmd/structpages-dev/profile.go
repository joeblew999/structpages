package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/coder/websocket"

	"github.com/jackielii/structpages/e2e"
)

// The profile command's two measurements: the Worker's CPU per request,
// timed as bench times it served, without the profiler, then a CPU profile
// over profileTime of the same requests.
const (
	profileServed = 2 * time.Second
	profileTime   = 3 * time.Second
)

// profile is the profile command, one step, the quick loop for making the
// Worker faster: the current folder's static-worker-profile build (the
// static-worker build with its function names) in workerd, sent every
// recorded page, first timed as bench times a build served, then profiled
// through workerd's inspector, as wrangler dev --inspect and Chrome's
// profiler do. It writes build/profile/static-worker.cpuprofile, which
// Chrome DevTools (Performance, Load profile) or speedscope.app show as a
// flame graph, and build/profile/static-worker.txt, the functions the CPU
// was spent in, which it prints too.
func profile(args []string) int {
	if len(args) != 0 {
		fmt.Fprintln(os.Stderr, "usage: structpages-dev profile")
		return 2
	}
	if err := profileWorker(newFolder(".")); err != nil {
		fmt.Fprintf(os.Stderr, "structpages-dev: %v\n", err)
		return 1
	}
	return 0
}

func profileWorker(f folder) error {
	const build = "static-worker-profile"
	reads, err := e2e.Reads(filepath.Join(f.dir, "testdata", "e2e.generated.txt"))
	if err != nil {
		return err
	}
	config := filepath.Join(f.abs, "build", build, "config.capnp")
	if _, serr := os.Stat(config); serr != nil {
		return fmt.Errorf("%s isn't built: mise run example:%s:build:%s", build, f.name, build)
	}
	port, err := freePort()
	if err != nil {
		return err
	}
	inspect := fmt.Sprintf("--inspector-addr=127.0.0.1:%d", port)
	s, err := startCmd(f, exec.Command("workerd", "serve", inspect, config), nil) //nolint:gosec // its build
	if err != nil {
		return err
	}
	defer s.stop()

	// The CPU per request first: the profiler, sampling, costs some.
	fmt.Printf("%s, %s: served...\n", f.name, build)
	lines, err := timeServerRuns(f, build, s, reads, 3, profileServed/3)
	if err != nil {
		return err
	}
	medians, pages := servedCPUOf([]byte(lines))
	var served strings.Builder
	for _, page := range pages {
		fmt.Fprintf(&served, "  %s: %.1f µs\n", page, medians[page])
	}

	fmt.Printf("%s, %s: profiled...\n", f.name, build)
	recorded, err := recordProfile(port, reads)
	if err != nil {
		return err
	}

	dir := filepath.Join(f.dir, "build", "profile")
	if merr := os.MkdirAll(dir, 0o750); merr != nil {
		return merr
	}
	file := filepath.Join(dir, "static-worker.cpuprofile")
	if werr := os.WriteFile(file, recorded, 0o600); werr != nil {
		return werr
	}
	top, err := topFunctions(recorded, 30)
	if err != nil {
		return err
	}
	text := "The Worker served, CPU per request (as bench times it, the median of 3 runs):\n" +
		served.String() + top
	if werr := os.WriteFile(filepath.Join(dir, "static-worker.txt"), []byte(text), 0o600); werr != nil {
		return werr
	}
	fmt.Print(text)
	rel, _ := filepath.Rel(f.dir, file)
	fmt.Printf("\nThe flame graph: open %s in Chrome DevTools (Performance, Load profile) or speedscope.app.\n", rel)
	return nil
}

// recordProfile records a CPU profile of the Worker whose inspector is on
// port, through it, while sending it reads for profileTime.
func recordProfile(port int, reads []e2e.Read) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	inspector, err := inspectorURL(ctx, port)
	if err != nil {
		return nil, err
	}
	conn, _, err := websocket.Dial(ctx, inspector, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.CloseNow() }()
	conn.SetReadLimit(1 << 30)
	call := cdp(ctx, conn)
	for _, step := range []struct {
		method string
		params any
	}{
		{"Profiler.enable", nil},
		{"Profiler.setSamplingInterval", map[string]int{"interval": 50}},
		{"Profiler.start", nil},
	} {
		if _, cerr := call(step.method, step.params); cerr != nil {
			return nil, cerr
		}
	}
	client := &http.Client{Timeout: time.Minute}
	for begin := time.Now(); time.Since(begin) < profileTime; {
		for i := range reads {
			req, rerr := http.NewRequest(http.MethodGet, "http://"+addr+reads[i].Path, http.NoBody) //nolint:noctx // local
			if rerr != nil {
				return nil, rerr
			}
			req.Header = reads[i].Header.Clone()
			resp, rerr := client.Do(req)
			if rerr != nil {
				return nil, rerr
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
	}
	result, err := call("Profiler.stop", nil)
	if err != nil {
		return nil, err
	}
	var stopped struct {
		Profile json.RawMessage `json:"profile"`
	}
	if jerr := json.Unmarshal(result, &stopped); jerr != nil {
		return nil, jerr
	}
	return stopped.Profile, nil
}

// freePort is a port nothing listens on, for workerd's inspector.
func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// inspectorURL is the WebSocket workerd's inspector on port serves the
// Worker's isolate at.
func inspectorURL(ctx context.Context, port int) (string, error) {
	list := fmt.Sprintf("http://127.0.0.1:%d/json/list", port)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, list, http.NoBody)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var targets []struct {
		URL string `json:"webSocketDebuggerUrl"`
	}
	if derr := json.NewDecoder(resp.Body).Decode(&targets); derr != nil {
		return "", derr
	}
	if len(targets) == 0 {
		return "", errors.New("workerd's inspector lists no Worker")
	}
	return targets[0].URL, nil
}

// cdp is a call over the Chrome DevTools Protocol on conn: a method, its
// params, and the result.
func cdp(ctx context.Context, conn *websocket.Conn) func(string, any) (json.RawMessage, error) {
	id := 0
	return func(method string, params any) (json.RawMessage, error) {
		id++
		msg, err := json.Marshal(map[string]any{"id": id, "method": method, "params": params})
		if err != nil {
			return nil, err
		}
		if werr := conn.Write(ctx, websocket.MessageText, msg); werr != nil {
			return nil, werr
		}
		for {
			_, data, rerr := conn.Read(ctx)
			if rerr != nil {
				return nil, rerr
			}
			var reply struct {
				ID     int             `json:"id"`
				Result json.RawMessage `json:"result"`
				Error  *struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			if json.Unmarshal(data, &reply) != nil || reply.ID != id {
				continue // an event, or another call's reply
			}
			if reply.Error != nil {
				return nil, fmt.Errorf("%s: %s", method, reply.Error.Message)
			}
			return reply.Result, nil
		}
	}
}

// topFunctions is a profile's n functions with the most CPU of their own,
// and the most in all (theirs and what they call), as text.
func topFunctions(profile json.RawMessage, n int) (string, error) {
	var p struct {
		Nodes []struct {
			ID        int `json:"id"`
			CallFrame struct {
				FunctionName string `json:"functionName"`
				URL          string `json:"url"`
			} `json:"callFrame"`
			Children []int `json:"children"`
		} `json:"nodes"`
		Samples    []int `json:"samples"`
		TimeDeltas []int `json:"timeDeltas"`
	}
	if err := json.Unmarshal(profile, &p); err != nil {
		return "", err
	}
	name := map[int]string{}
	parent := map[int]int{}
	for _, node := range p.Nodes {
		fn := node.CallFrame.FunctionName
		if fn == "" {
			fn = "(anonymous " + filepath.Base(node.CallFrame.URL) + ")"
		}
		name[node.ID] = fn
		for _, c := range node.Children {
			parent[c] = node.ID
		}
	}
	self, total := map[string]float64{}, map[string]float64{}
	var all float64
	for i, sample := range p.Samples {
		if i >= len(p.TimeDeltas) || name[sample] == "(idle)" {
			continue
		}
		t := float64(p.TimeDeltas[i])
		all += t
		self[name[sample]] += t
		seen := map[string]bool{}
		for id, ok := sample, true; ok; id, ok = parent[id] {
			if fn := name[id]; !seen[fn] {
				seen[fn] = true
				total[fn] += t
			}
		}
	}
	table := func(title string, m map[string]float64) string {
		type entry struct {
			fn string
			t  float64
		}
		es := make([]entry, 0, len(m))
		for fn, t := range m {
			es = append(es, entry{fn, t})
		}
		slices.SortFunc(es, func(a, b entry) int { return int(b.t - a.t) })
		var b strings.Builder
		fmt.Fprintf(&b, "\n%s, %% of the isolate's busy time:\n", title)
		for _, e := range es[:min(n, len(es))] {
			fmt.Fprintf(&b, "%6.1f%%  %s\n", 100*e.t/all, e.fn)
		}
		return b.String()
	}
	return table("Its own CPU", self) + table("With what it calls", total), nil
}
