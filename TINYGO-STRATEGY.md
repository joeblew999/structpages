# TinyGo strategy

What this fork's `tinygo` branch is trying to do, where it stands, and what to do next. It is about the TinyGo work
only; the library itself is documented in [README.md](./README.md) and [docs/](./docs). Last updated 2026-10-28.

## Goal

A structpages app runs **three ways**, and behaves the same in each:

| Way | Toolchain | How structpages reads the app's methods |
|---|---|---|
| **dynamic** | standard Go | `reflect`, at run time, as upstream always has (the default) |
| **static** | standard Go, `-tags structpages_static` | code that `structpages-static generate` generates from the app's source |
| **static-worker** | TinyGo, a Cloudflare Worker (WebAssembly) | the same generated code |

Static is a real choice for Go users, not just a way to test TinyGo's code path: an app can run without method
reflection on the standard toolchain, and it must stay correct there. TinyGo has no choice: its `reflect` can't do what
structpages needs.

"The same" is measured, not assumed. For the library, `mise run test` runs its one test suite all three ways and shows,
for each, how many passed and every test it skipped, with why. For the examples, `mise run examples` does the same for
each, and every example's e2e test compares its responses all three ways (see the strategy). Parity is no differences
left.

## How it works

TinyGo's `reflect` can't list or call methods, read a function type's parameters, or name a function. structpages
needs all four, so everything it reads about methods and functions goes through one small set of functions with two
implementations, picked by build tag:

- `methods_dynamic.go` (dynamic): `reflect`, exactly as before.
- `methods_static.go` (static and static-worker): what `structpages-static generate` (`tools/static`) generates. It writes
  `structpages_static.generated.go` into each package with pages, next to the pages: generated code can only name a
  package's unexported types from inside that package, as with gsx's `.x.go`. The files only build with the tag or
  under TinyGo, so the dynamic build ignores them whether they exist or not.

The generator replaces method reflection only. Struct fields, values and dependency injection still use `reflect` in
all three ways, and the generated code still calls through `reflect.Value`.

## Where it stands

**Library tests** (`mise run test`):

| | Passed | Skipped | Failed |
|---|---|---|---|
| reflect | 796 | 0 | 0 |
| generated | 796 | 0 | 0 |
| TinyGo 0.43 | 795 | 0 | 0 |

Nothing is skipped any more. It used to be 14 under TinyGo:

- **11 routing tests**, since TinyGo's `net/http` `ServeMux` routes like Go 1.21: no `GET /path`, no `{id}`, no `{$}`
  ([tinygo-org/net#55](https://github.com/tinygo-org/net/issues/55)). They run now: in the generated and TinyGo builds
  `Mount` routes through `internal/servemux`, Go's own `ServeMux` (see the strategy).
- **2 function-literal tests**: the generated code can only name declared functions. They now check the error the
  static builds give instead, or use a declared method.
- **1 generic page type**: the generator now writes code for each instantiation the code uses (`genericPage[int]`).

The one test fewer under TinyGo is `internal/servemux`'s `TestSameAsNetHTTP`, built only on the standard toolchain: it
compares the copy with Go 1.26's own `net/http` mux, which TinyGo doesn't have.

**Examples:**

- **Build:** all eight build all three ways. TinyGo's WebAssembly (`-target wasm`) is 742 KB to 1.4 MB per example
  (measured 2026-10-28; `mise run example:todo:serve:static-worker` prints todo's, 1.4 MB, when it serves it).
- **Tests:** six of the eight have none (blog, html-template, htmx, htmx-render-target, simple, todo), so their test
  runs prove only that they compile. `url-validation`'s tests pass dynamic and static, and **fail under
  TinyGo**: `/foundations/` serves the home page, because TinyGo's mux can't match `/{$}` or `/{slug}`. That is the
  routing gap above, seen in a real app.
- **Serving:** a TinyGo build can't serve on a desktop at all: `http.ListenAndServe` fails with `Netdev not set`
  (TinyGo's networking needs a board's network driver). On WebAssembly hosts the host hands the app its requests
  instead, so the TinyGo way has to run in such a host, such as `workerd`, Cloudflare's Workers runtime, locally.
- **Shape:** every example builds its app inside `main()`, next to `ListenAndServe`, so nothing can reach its
  `http.Handler` without starting a server.

**Benchmarks** (`mise run bench`, results in [BENCH_GENERATED.md](./BENCH_GENERATED.md)), on an
Apple M2 Pro:

- **Static vs dynamic: about even** (geometric mean 2% faster). Faster where reflection is costly,
  naming a function for `ID` (−39% to −63%, half the bytes). Slower where it's cheap: `Mount` +9–15%, `URLFor`
  +6–19%, `isComponent` +63%, because each generated lookup is a map lookup keyed by `reflect.Type` where `reflect`
  reads a field.
- **Static-worker vs dynamic: about 3.4× the time and 2–3× the bytes.** Generated costs about the same as reflection
  on Go, so this gap is TinyGo's compiler, runtime and garbage collector, not structpages. A simple GET is 2.5µs. These
  TinyGo runs are native binaries, not WebAssembly.
- **A real page** (`mise run bench:examples`, into [examples/todo/BENCH_GENERATED.md](./examples/todo/BENCH_GENERATED.md)):
  `todo`'s `GET /` takes 9.6µs with reflection, **7.4µs generated (23% faster, a fifth fewer allocations)**, and
  30µs under TinyGo.

**Generator gaps** (`generator_gaps_test.go`: one test per app pattern, run all three ways by the suite; reflection's
answer is the one the other two must give):

| Pattern | Result |
|---|---|
| request-arg provider of a **named function type** (`type P func(*http.Request) (T, error)`) | **was a gap**: worked with reflection, failed at `Mount` generated and under TinyGo. Fixed: the generator writes code for the named type |
| provider that is a method value (`svc.CurrentUser`) | same all three ways |
| a page's `RequestArgs` returning a function literal | same all three ways |
| route fields of an alias type, or a pointer to a page | same all three ways |
| `ID` of a method promoted from an embedded struct | same all three ways (an error in each: the method's receiver is no page) |
| `Props` returning an interface; a component on a pointer receiver | same all three ways |
| generic page types | **was a gap**: skipped. Fixed: the generator writes code for each instantiation used (`genericPage[int]`), and warns about one used only inside generic code |
| function literals passed to `ID`/`RenderTarget.Is` | **can't be**: generated code can name only declared functions. The static builds return a clear error, which the tests check |

The generator gives no warnings on any of the eight examples.

## Strategy

In order:

1. **Check every example all three ways, automatically.** Manual checks don't scale to eight examples times three
   ways. Three parts, the first two needed before anything else here can be trusted:
   - **Give each example an `http.Handler`.** Split each `main()` into a function that builds the app and returns its
     handler, and a `main` that only serves it: the shape [go-htmx4](https://github.com/joeblew999/go-htmx4) uses
     (`app.Handler()`, `main() { transport.Run(app.Handler()) }`). It touches all eight upstream examples, a few lines
     each.
   - **One differential checker, in-process.** A small Go package that takes a handler, crawls it from `/` through
     `httptest` (following links, forms and htmx attributes, and sending htmx's request headers so partials are
     covered), and records every response. Each example runs it as an ordinary test, so it runs all three ways:
     `go test`, `go test -tags structpages_static`, `tinygo test`. The reflection run is the reference: any response
     that differs in the other two is a failure. It finds each example's routes by itself, so there are no per-example
     lists to keep up, and it needs no sockets, so TinyGo can run it.

     **Status: built, and tried on one example first.** It is the `e2e` package (`e2e.Crawl`), in the library's module next
     to `generated`, as it needs nothing outside the standard library. `todo` has its `handler()` and a one-line `e2e_test.go`; its recording is
     `testdata/e2e.generated.txt`. The crawl found all eight of its requests by itself: the page, `POST /add`, and
     toggle and delete for each todo, as htmx partials. `mise run examples` shows: reflection pass, generated
     pass, **TinyGo fail at `POST /toggle/3`**, which answers 200 without toggling: the `{id}` wildcard is lost under
     TinyGo's mux. Once this shape is agreed, the other seven examples follow it.
   - **Later, a browser.** For click-through checks of htmx behaviour, chromedp with mise-pinned Chrome for Testing
     (`chrome-headless-shell`), as go-htmx4's `e2e` module does.
2. **Make routing work under TinyGo.** It is most of the library's gap and it breaks real apps (`url-validation`).
   structpages only tells TinyGo users to bring a mux that understands Go 1.22 patterns, and its tests skip instead of
   using one. Give TinyGo builds such a mux, so the 11 routing tests run and the examples route. An earlier attempt
   copied Go's `ServeMux` into the library and was reverted the same day; whatever lands, the checker in step 1 says
   whether it works. In parallel, follow [tinygo-org/net#56](https://github.com/tinygo-org/net/pull/56), which fixes
   method patterns but not escaped slashes.

   **Status: routing works in `Mount`.** `internal/servemux` is Go 1.26's `ServeMux` as Go wrote it, with Go's tests,
   which pass all three ways. In the generated and TinyGo builds `Mount` registers its routes on one and hands the
   app's mux only the mount's prefix (`routes_static.go`); the dynamic build registers on the app's mux as before
   (`routes_dynamic.go`). So apps keep `http.NewServeMux()`, and generated and TinyGo differ only in their compiler.
   An app keeps its ordinary `main` and adds one built only for the Worker (`main_worker.go`, `workers.Serve`),
   and `tools/workers`' `structpages-workers` builds the Worker, glue and all. `todo`'s e2e test passes all three ways, and served in workerd its toggle and delete work and its state
   is kept. The other examples follow when they get `todo`'s shape.
3. **Run the TinyGo way where it will really run.** Serve each example's handler from `workerd`, locally, through
   workers-go (go-htmx4 pins workerd and TinyGo in mise for this). Then the checker can also crawl it over HTTP, and
   the WebAssembly's start-up time and size per request can be measured; for Workers those matter more than
   microseconds per request.
4. **Keep the fork small.** The branch changes 33 of upstream's files, and step 1 adds the eight examples. Most of the
   library changes are mechanical: calls into `reflect` replaced by the wrapper functions, which change nothing for
   dynamic. Offering that indirection upstream would shrink the fork to the generator, the generated-code
   support and the TinyGo docs. That is a conversation to have with the maintainer before sending anything; nothing
   has gone upstream.
5. **Accept the documented limits.** Generic page types and function literals stay unsupported in static builds;
   the generator warns about them and `docs/static.md` says so.
6. **Speed up generated code only if a profile asks for it.** The generator knows every page type, so it could write
   direct calls and cache each method's entry instead of looking it up per call. On TinyGo the cost is TinyGo itself.
7. **Delete the generated-code support when TinyGo catches up.** Once TinyGo's `reflect` can list and call methods
   ([tinygo#3862](https://github.com/tinygo-org/tinygo/issues/3862)) and name functions
   ([tinygo#4379](https://github.com/tinygo-org/tinygo/issues/4379)), the comment at the top of `methods_static.go`
   lists what to remove, if static isn't worth keeping on its own by then. `mise run test` with no
   generated files is the check.

## Risks to keep in mind

- `generated/key_tinygo.go` reads TinyGo's internal layout of a function value through `unsafe`. A TinyGo release that
  changes it breaks `ID` silently. The TinyGo test run would catch it, so run it on every TinyGo upgrade.
- A request-arg provider declared with a named function type (`type P func(*http.Request) (T, error)`) probably gets
  no generated code, so it would work on Go and fail at `Mount` on TinyGo. Untested.
- Found upstream, not caused here and left alone: `tools/lint`'s `TestRegion_Integration` fails on upstream `main`;
  `mise run lint:examples` reports three hard-coded URLs in `examples/blog`; the README says MIT where `LICENSE` is
  BSD 3-Clause.

## Checking progress

```bash
mise run test                       # library: its tests three ways, with every skip
mise run examples                   # every example's tests three ways, a line each
mise run example:todo:test          # todo's tests three ways
mise run example:todo:serve:static-worker  # serve todo as WebAssembly in workerd (and :serve:dynamic, :serve:static)
mise run bench:examples             # every example's pages three ways, into its BENCH_GENERATED.md
mise run bench                      # library: its benchmarks three ways, into BENCH_GENERATED.md
mise run ci:go                      # everything upstream's CI checks on Go
```
