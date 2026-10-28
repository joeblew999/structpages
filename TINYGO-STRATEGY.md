# TinyGo strategy

What this fork's `tinygo` branch is trying to do, where it stands, and what to do next. It is about the TinyGo work
only; the library itself is documented in [README.md](./README.md) and [docs/](./docs). Last updated 2026-10-28.

## Goal

A structpages app runs **three ways**, and behaves the same in each:

| Way | Toolchain | How structpages reads the app's methods |
|---|---|---|
| **Go, reflection** | standard Go | `reflect`, as upstream always has (the default) |
| **Go, generated** | standard Go, `-tags structpages_tables` | code that `structpages-gen` generates from the app's source |
| **TinyGo, generated** | TinyGo, for WebAssembly hosts such as Cloudflare Workers | the same generated code |

Go, generated is a real choice for Go users, not just a way to test TinyGo's code path: an app can run without method
reflection on the standard toolchain, and it must stay correct there. TinyGo has no choice: its `reflect` can't do what
structpages needs.

"The same" is measured, not assumed. For the library, `mise run parity` runs its one test suite on Go and on TinyGo
and lists every test that passes on Go but not on TinyGo. For the examples, every example is built, tested and (see
the strategy) checked all three ways. Parity is no differences left.

## How it works

TinyGo's `reflect` can't list or call methods, read a function type's parameters, or name a function. structpages
needs all four, so everything it reads about methods and functions goes through one small set of functions with two
implementations, picked by build tag:

- `reflect_runtime.go` (Go, reflection): `reflect`, exactly as before.
- `reflect_tables.go` (Go, generated, and TinyGo): what `structpages-gen` (`tools/gen`) generates. It writes
  `structpages_tables.generated.go` into each package with pages, next to the pages: generated code can only name a
  package's unexported types from inside that package, as with gsx's `.x.go`. The files only build with the tag or
  under TinyGo, so the reflection build ignores them whether they exist or not.

The generator replaces method reflection only. Struct fields, values and dependency injection still use `reflect` in
all three ways, and the generated code still calls through `reflect.Value`.

## Where it stands

**Library tests** (`mise run parity`):

| | Passed | Skipped | Failed |
|---|---|---|---|
| Go | 765 | 0 | 0 |
| TinyGo 0.43 | 731 | 14 | 0 |

The 14 skips:

- **11 routing.** TinyGo's `net/http` `ServeMux` routes like Go 1.21: no `GET /path`, no `{id}`, no `{$}`
  ([tinygo-org/net#55](https://github.com/tinygo-org/net/issues/55)). Every test that serves a real request through a
  mux skips: handlers, middleware, subpath mounting, `URLFor` from request params.
- **2 function literals** passed to `ID`/`RenderTarget.Is`. The generator can only name declared functions.
- **1 generic page type.** The generator can't write code for generic types.

**Examples:**

- **Build:** all eight build all three ways. TinyGo's WebAssembly (`-target wasm`) is 742 KB to 1.4 MB per example
  (`mise run build:examples:tinygo`).
- **Tests:** six of the eight have none (blog, html-template, htmx, htmx-render-target, simple, todo), so their test
  runs prove only that they compile. `url-validation`'s tests pass on Go, reflection and generated, and **fail under
  TinyGo**: `/foundations/` serves the home page, because TinyGo's mux can't match `/{$}` or `/{slug}`. That is the
  routing gap above, seen in a real app.
- **Serving:** a TinyGo build can't serve on a desktop at all: `http.ListenAndServe` fails with `Netdev not set`
  (TinyGo's networking needs a board's network driver). On WebAssembly hosts the host hands the app its requests
  instead, so the TinyGo way has to run in such a host, such as `workerd`, Cloudflare's Workers runtime, locally.
- **Shape:** every example builds its app inside `main()`, next to `ListenAndServe`, so nothing can reach its
  `http.Handler` without starting a server.

**Benchmarks** (`mise run bench:compare`, results in [BENCH-COMPARE.GENERATED.md](./BENCH-COMPARE.GENERATED.md)), on an
Apple M2 Pro:

- **Go, generated vs Go, reflection: about even** (geometric mean 2% faster). Faster where reflection is costly,
  naming a function for `ID` (−39% to −63%, half the bytes). Slower where it's cheap: `Mount` +9–15%, `URLFor`
  +6–19%, `isComponent` +63%, because each generated lookup is a map lookup keyed by `reflect.Type` where `reflect`
  reads a field.
- **TinyGo vs Go, reflection: about 3.4× the time and 2–3× the bytes.** Generated costs about the same as reflection
  on Go, so this gap is TinyGo's compiler, runtime and garbage collector, not structpages. A simple GET is 2.5µs. These
  TinyGo runs are native binaries, not WebAssembly.

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
     `go test`, `go test -tags structpages_tables`, `tinygo test`. The reflection run is the reference: any response
     that differs in the other two is a failure. It finds each example's routes by itself, so there are no per-example
     lists to keep up, and it needs no sockets, so TinyGo can run it.
   - **Later, a browser.** For click-through checks of htmx behaviour, chromedp with mise-pinned Chrome for Testing
     (`chrome-headless-shell`), as go-htmx4's `e2e` module does.
2. **Make routing work under TinyGo.** It is most of the library's gap and it breaks real apps (`url-validation`).
   structpages only tells TinyGo users to bring a mux that understands Go 1.22 patterns, and its tests skip instead of
   using one. Give TinyGo builds such a mux, so the 11 routing tests run and the examples route. An earlier attempt
   copied Go's `ServeMux` into the library and was reverted the same day; whatever lands, the checker in step 1 says
   whether it works. In parallel, follow [tinygo-org/net#56](https://github.com/tinygo-org/net/pull/56), which fixes
   method patterns but not escaped slashes.
3. **Run the TinyGo way where it will really run.** Serve each example's handler from `workerd`, locally, through
   workers-go (go-htmx4 pins workerd and TinyGo in mise for this). Then the checker can also crawl it over HTTP, and
   the WebAssembly's start-up time and size per request can be measured; for Workers those matter more than
   microseconds per request.
4. **Keep the fork small.** The branch changes 33 of upstream's files, and step 1 adds the eight examples. Most of the
   library changes are mechanical: calls into `reflect` replaced by the wrapper functions, which change nothing for
   Go, reflection. Offering that indirection upstream would shrink the fork to the generator, the generated-code
   support and the TinyGo docs. That is a conversation to have with the maintainer before sending anything; nothing
   has gone upstream.
5. **Accept the documented limits.** Generic page types and function literals stay unsupported in generated builds;
   the generator warns about them and `docs/tinygo.md` says so.
6. **Speed up generated code only if a profile asks for it.** The generator knows every page type, so it could write
   direct calls and cache each method's entry instead of looking it up per call. On TinyGo the cost is TinyGo itself.
7. **Delete the generated-code support when TinyGo catches up.** Once TinyGo's `reflect` can list and call methods
   ([tinygo#3862](https://github.com/tinygo-org/tinygo/issues/3862)) and name functions
   ([tinygo#4379](https://github.com/tinygo-org/tinygo/issues/4379)), the comment at the top of `reflect_tables.go`
   lists what to remove, if Go, generated isn't worth keeping on its own by then. `mise run parity` with no generated
   files is the check.

## Risks to keep in mind

- `tables/key_tinygo.go` reads TinyGo's internal layout of a function value through `unsafe`. A TinyGo release that
  changes it breaks `ID` silently. The TinyGo test run would catch it, so run it on every TinyGo upgrade.
- A request-arg provider declared with a named function type (`type P func(*http.Request) (T, error)`) probably gets
  no generated code, so it would work on Go and fail at `Mount` on TinyGo. Untested.
- Found upstream, not caused here and left alone: `tools/lint`'s `TestRegion_Integration` fails on upstream `main`;
  `mise run lint:examples` reports three hard-coded URLs in `examples/blog`; the README says MIT where `LICENSE` is
  BSD 3-Clause.

## Checking progress

```bash
mise run parity                 # library: what still differs between Go and TinyGo
mise run test:examples          # examples' tests, Go, reflection
mise run test:examples:tables   # examples' tests, Go, generated
mise run test:examples:tinygo   # examples' tests, TinyGo
mise run build:examples:tinygo  # every example built with TinyGo, natively and as WebAssembly, with sizes
mise run bench:compare          # Go, reflection vs Go, generated vs TinyGo, into BENCH-COMPARE.GENERATED.md
mise run ci:go                  # everything upstream's CI checks on Go
```
