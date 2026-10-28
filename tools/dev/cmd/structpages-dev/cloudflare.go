package main

import (
	"bufio"
	"bytes"
	"cmp"
	"encoding/json"
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

// The Cloudflare measurement is a check, half a minute: it sends each
// recorded page cloudflareWarm times to warm the Worker's isolates, then
// cloudflareRequests times, one at a time. wrangler tail logs a sample of
// them, a smaller one the faster they come, so the report says how many it
// logged.
const (
	cloudflareWarm     = 50
	cloudflareRequests = 200
	benchHeader        = "X-Structpages-Bench" // which run a request is from, for the tail to pick out
)

// deployed is the file deploy writes the Worker's URL to, in its build.
const deployed = "deployed.txt"

// deploy is the deploy command: the current folder's static-worker build
// deployed to Cloudflare by wrangler, as its wrangler.jsonc says (wrangler
// logged in by mise's cloudflare:login), and the URL Cloudflare serves it at
// written to the build's deployed.txt, for bench cloudflare.
func deploy(args []string) int {
	if len(args) != 0 {
		fmt.Fprintln(os.Stderr, "usage: structpages-dev deploy")
		return 2
	}
	build := filepath.Join(newFolder(".").dir, "build", "static-worker")
	var out bytes.Buffer
	cmd := exec.Command("wrangler", "deploy", "-c", filepath.Join(build, "wrangler.jsonc")) //nolint:gosec // its build
	cmd.Stdout, cmd.Stderr = io.MultiWriter(os.Stdout, &out), os.Stderr
	if err := cmd.Run(); err != nil {
		fail(fmt.Errorf("wrangler deploy: %w (logged in? mise run cloudflare:login)", err))
	}
	u := regexp.MustCompile(`https://[a-z0-9.-]+\.workers\.dev`).FindString(out.String())
	if u == "" {
		fail(errors.New("wrangler deploy printed no workers.dev URL"))
	}
	if err := os.WriteFile(filepath.Join(build, deployed), []byte(u+"\n"), 0o600); err != nil {
		fail(err)
	}
	fmt.Println("deployed at", u)
	return 0
}

// tailEvent is what wrangler tail --format json gives for a request: the
// CPU and wall time Cloudflare logged for it, in whole milliseconds.
type tailEvent struct {
	CPUTime  float64 `json:"cpuTime"`
	WallTime float64 `json:"wallTime"`
	Outcome  string  `json:"outcome"`
	Event    struct {
		Request struct {
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
		} `json:"request"`
	} `json:"event"`
}

// benchCloudflare is bench cloudflare, one step: the folder's deployed
// Worker sent every recorded page, as timeServer sends a local build, while
// wrangler tail reads what Cloudflare logged for each request, into
// build/bench/cloudflare.txt, a line a request: its page, the CPU and wall
// time Cloudflare logged, the time it took at this client, and the kind of
// runtime that answered (x-go-runtime).
func benchCloudflare(f folder) error {
	build := filepath.Join(f.dir, "build", "static-worker")
	data, err := os.ReadFile(filepath.Join(build, deployed))
	if err != nil {
		return fmt.Errorf("%w: deploy it first (mise run example:%s:deploy)", err, f.name)
	}
	reads, err := e2e.Reads(filepath.Join(f.dir, "testdata", "e2e.generated.txt"))
	if err != nil {
		return err
	}
	c := &cloudflareRun{
		f: f, base: strings.TrimSpace(string(data)), run: strconv.FormatInt(time.Now().UnixNano(), 36),
		client: &http.Client{Timeout: time.Minute}, samples: map[string]*cloudflareSample{},
	}
	// Only the requests this run tags, picked out at Cloudflare's end.
	tail := exec.Command("wrangler", "tail", "-c", filepath.Join(build, "wrangler.jsonc"), //nolint:gosec // its build
		"--format", "json", "--header", benchHeader)
	stdout, err := tail.StdoutPipe()
	if err != nil {
		return err
	}
	tail.Stderr = os.Stderr
	if serr := tail.Start(); serr != nil {
		return serr
	}
	defer func() { _ = tail.Process.Kill(); _ = tail.Wait() }()
	c.events = make(chan tailEvent, 4096)
	go readEvents(stdout, c.events)

	if werr := c.waitForTail(&reads[0]); werr != nil {
		return werr
	}
	for i := range reads {
		if serr := c.sendPage(&reads[i]); serr != nil {
			return serr
		}
	}
	if lerr := c.readLogged(); lerr != nil {
		return lerr
	}
	return c.write()
}

// cloudflareRun is one bench cloudflare: its requests, by the tag each was
// sent with, in order.
type cloudflareRun struct {
	f         folder
	base, run string // the Worker's URL, and this run's tag
	client    *http.Client
	events    chan tailEvent
	samples   map[string]*cloudflareSample
	order     []string
}

// cloudflareSample is a request, and what Cloudflare logged for it.
type cloudflareSample struct {
	page, kind string
	client     time.Duration
	cpu, wall  float64
	logged     bool
}

// send sends r, tagged with tag unless it is "", and returns the time it
// took and the kind of runtime that answered.
func (c *cloudflareRun) send(r *e2e.Read, tag string) (time.Duration, string, error) {
	req, err := http.NewRequest(http.MethodGet, c.base+r.Path, http.NoBody) //nolint:noctx // a benchmark
	if err != nil {
		return 0, "", err
	}
	req.Header = r.Header.Clone()
	if tag != "" {
		req.Header.Set(benchHeader, tag)
	}
	req.Header.Set("X-Go-Runtime", "1")
	begin := time.Now()
	resp, err := c.client.Do(req)
	if err != nil {
		return 0, "", err
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	kind, _, _ := strings.Cut(resp.Header.Get("X-Go-Runtime"), ";")
	return time.Since(begin), kind, nil
}

// waitForTail waits for wrangler tail to be connected: until a request sent
// after it started shows in it.
func (c *cloudflareRun) waitForTail(r *e2e.Read) error {
	fmt.Printf("%s, cloudflare: waiting for wrangler tail on %s...\n", c.f.name, c.base)
	probe := c.run + "-probe"
	for deadline := time.Now().Add(2 * time.Minute); time.Now().Before(deadline); {
		if _, _, err := c.send(r, probe); err != nil {
			return err
		}
		for wait := time.After(2 * time.Second); ; {
			select {
			case e, ok := <-c.events:
				if !ok {
					return errors.New("wrangler tail stopped")
				}
				if e.Event.Request.Headers[strings.ToLower(benchHeader)] == probe {
					return nil
				}
				continue
			case <-wait:
			}
			break
		}
	}
	return errors.New("wrangler tail showed none of the requests sent: is it logged in? (mise run cloudflare:login)")
}

// sendPage sends r cloudflareWarm times, untagged, then cloudflareRequests
// times, each tagged, one at a time.
func (c *cloudflareRun) sendPage(r *e2e.Read) error {
	fmt.Printf("%s, cloudflare: %s, %d to warm, then %d...\n", c.f.name, r.Name, cloudflareWarm, cloudflareRequests)
	for range cloudflareWarm {
		if _, _, err := c.send(r, ""); err != nil {
			return err
		}
	}
	for n := range cloudflareRequests {
		tag := fmt.Sprintf("%s-%s-%d", c.run, r.Name, n)
		took, kind, err := c.send(r, tag)
		if err != nil {
			return err
		}
		c.samples[tag] = &cloudflareSample{page: r.Name, kind: kind, client: took}
		c.order = append(c.order, tag)
	}
	return nil
}

// readLogged reads what Cloudflare logged for the requests sent, until each
// has shown or none has for ten seconds: Cloudflare logs a request a moment
// after it is answered, and the tail logs only a sample.
func (c *cloudflareRun) readLogged() error {
	fmt.Printf("%s, cloudflare: reading what Cloudflare logged...\n", c.f.name)
	missing := len(c.samples)
	for quiet := time.After(10 * time.Second); missing > 0; {
		select {
		case e, open := <-c.events:
			if !open {
				return errors.New("wrangler tail stopped")
			}
			if s, ok := c.samples[e.Event.Request.Headers[strings.ToLower(benchHeader)]]; ok && !s.logged {
				s.cpu, s.wall, s.logged = e.CPUTime, e.WallTime, true
				missing--
				quiet = time.After(10 * time.Second)
			}
		case <-quiet:
			fmt.Printf("%s, cloudflare: wrangler tail logged %d of %d requests\n",
				c.f.name, len(c.samples)-missing, len(c.samples))
			missing = 0
		}
	}
	return nil
}

// write writes build/bench/cloudflare.txt.
func (c *cloudflareRun) write() error {
	var out strings.Builder
	fmt.Fprintf(&out, "# url=%s at=%s sent=%d\n", c.base, time.Now().UTC().Format("2006-01-02T15:04Z"), len(c.samples))
	for _, tag := range c.order {
		s := c.samples[tag]
		if !s.logged {
			continue
		}
		fmt.Fprintf(&out, "%s cpu-ms=%g wall-ms=%g client-ms=%.1f runtime=%s\n",
			s.page, s.cpu, s.wall, float64(s.client.Microseconds())/1e3, cmp.Or(s.kind, "?"))
	}
	dir := filepath.Join(c.f.dir, "build", "bench")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "cloudflare.txt"), []byte(out.String()), 0o600)
}

// readEvents sends each JSON object wrangler tail writes to events: it
// writes them one after another, each over several lines, with anything
// else it prints between them skipped.
func readEvents(r io.Reader, events chan<- tailEvent) {
	br := bufio.NewReader(r)
	var obj []byte
	depth, inString, escaped := 0, false, false
	for {
		c, err := br.ReadByte()
		if err != nil {
			close(events)
			return
		}
		if depth == 0 && c != '{' {
			continue
		}
		obj = append(obj, c)
		switch {
		case escaped:
			escaped = false
		case inString && c == '\\':
			escaped = true
		case c == '"':
			inString = !inString
		case !inString && c == '{':
			depth++
		case !inString && c == '}':
			depth--
			if depth == 0 {
				var e tailEvent
				if json.Unmarshal(obj, &e) == nil {
					events <- e
				}
				obj = obj[:0]
			}
		}
	}
}

// cloudflareSummary is the report's section for build/bench/cloudflare.txt,
// or "" without it.
func cloudflareSummary(app, file string, labels map[string]string) string {
	data, err := os.ReadFile(file)
	if err != nil {
		return ""
	}
	type page struct {
		cpu, wall, client []float64
		kinds             map[string]int
	}
	pages := map[string]*page{}
	var names []string
	var where, at, sent string
	header := regexp.MustCompile(`^# url=(\S+) at=(\S+) sent=(\d+)$`)
	line := regexp.MustCompile(`^(\S+) cpu-ms=(\S+) wall-ms=(\S+) client-ms=(\S+) runtime=(\S+)$`)
	for _, l := range strings.Split(string(data), "\n") {
		if m := header.FindStringSubmatch(l); m != nil {
			where, at, sent = m[1], m[2], m[3]
			continue
		}
		m := line.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		p := pages[m[1]]
		if p == nil {
			p = &page{kinds: map[string]int{}}
			pages[m[1]] = p
			names = append(names, m[1])
		}
		cpu, _ := strconv.ParseFloat(m[2], 64)
		wall, _ := strconv.ParseFloat(m[3], 64)
		client, _ := strconv.ParseFloat(m[4], 64)
		p.cpu, p.wall, p.client = append(p.cpu, cpu), append(p.wall, wall), append(p.client, client)
		p.kinds[m[5]]++
	}
	if len(names) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "The static-worker build deployed at %s (example:%s:deploy), timed %s: each recorded page\n"+
		"sent %d times to warm its isolates, then %d times, one at a time, %s in all (example:%s:bench:cloudflare).\n"+
		"The CPU and wall time are what Cloudflare logged for each request, in whole milliseconds: what it bills.\n"+
		"wrangler tail, which reads them, logs a sample of the requests, so the table counts those it logged. The\n"+
		"time at the client is mostly the network between this machine and Cloudflare.\n\n",
		where, app, at, cloudflareWarm, cloudflareRequests, sent, app)
	b.WriteString("| page | requests logged | CPU, mean | CPU, median | CPU, 90th % | CPU, most | wall, median " +
		"| at the client, median | runtimes |\n")
	b.WriteString("|---|---|---|---|---|---|---|---|---|\n")
	for _, name := range names {
		p := pages[name]
		var kinds []string
		for k, n := range p.kinds {
			kinds = append(kinds, fmt.Sprintf("%s %d", k, n))
		}
		slices.Sort(kinds)
		fmt.Fprintf(&b, "| `%s` | %d | %.2f ms | %g ms | %g ms | %g ms | %g ms | %.0f ms | %s |\n",
			cmp.Or(labels[name], name), len(p.cpu), mean(p.cpu), quantile(p.cpu, 0.5), quantile(p.cpu, 0.9),
			slices.Max(p.cpu), quantile(p.wall, 0.5), quantile(p.client, 0.5), strings.Join(kinds, ", "))
	}
	b.WriteString("\nCPU per request, how many requests logged each:\n\n")
	for _, name := range names {
		counts := map[float64]int{}
		for _, v := range pages[name].cpu {
			counts[v]++
		}
		var values []float64
		for v := range counts {
			values = append(values, v)
		}
		slices.Sort(values)
		var parts []string
		for _, v := range values {
			parts = append(parts, fmt.Sprintf("%g ms: %d", v, counts[v]))
		}
		fmt.Fprintf(&b, "- `%s`: %s\n", cmp.Or(labels[name], name), strings.Join(parts, ", "))
	}
	return b.String()
}

func mean(vs []float64) float64 {
	var sum float64
	for _, v := range vs {
		sum += v
	}
	return sum / float64(len(vs))
}

// quantile is the q-quantile of vs, the nearest rank.
func quantile(vs []float64, q float64) float64 {
	s := slices.Clone(vs)
	slices.Sort(s)
	return s[min(len(s)-1, int(q*float64(len(s))))]
}
