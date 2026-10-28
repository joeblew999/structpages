# TinyGo strategy

What this fork's `tinygo` branch is trying to do, where it stands, and what to do next. It is about the TinyGo work
only; the library itself is documented in [README.md](./README.md) and [docs/](./docs). Last updated 2026-10-28.

## Goal

The same structpages app builds and behaves the same on standard Go and on TinyGo, so it can ship as small
WebAssembly to hosts such as Cloudflare Workers. "The same" is measured, not assumed: `mise run parity` runs the one
test suite on both and lists every test that passes on Go but not on TinyGo. Parity is that list being empty.

## How it works

TinyGo's `reflect` can't list or call methods, read a function type's parameters, or name a function. structpages
needs all four, so everything it reads about methods and functions goes through one small set of functions with two
implementations, picked by build tag:

- `reflect_runtime.go` (standard Go, the default): `reflect`, exactly as before.
- `reflect_tables.go` (TinyGo, or Go with `-tags structpages_tables`): tables that `tools/gen` (structpages-gen) writes ahead
  of time from the app's source.

The generator replaces method reflection only. Struct fields, values and dependency injection still use `reflect` in
both builds, and the generated code still calls through `reflect.Value`.

## Where it stands

Tests (`mise run parity`):

| | Passed | Skipped | Failed |
|---|---|---|---|
| standard Go | 765 | 0 | 0 |
| TinyGo 0.43 | 731 | 14 | 0 |

The 14 skips:

- **11 routing.** TinyGo's `net/http` `ServeMux` routes like Go 1.21: no `GET /path`, no `{id}`, no `{$}`
  ([tinygo-org/net#55](https://github.com/tinygo-org/net/issues/55)). Every test that serves a real request through a
  mux skips: handlers, middleware, subpath mounting, `URLFor` from request params.
- **2 function literals** passed to `ID`/`RenderTarget.Is`. The generator can only name declared functions.
- **1 generic page type.** The generator can't write tables for generic types.

Benchmarks (`mise run bench:compare`, results in [BENCH-COMPARE.GENERATED.md](./BENCH-COMPARE.GENERATED.md)), on an
Apple M2 Pro:

- **Tables vs reflect, both on standard Go: about even** (geometric mean 2% faster). Faster where reflect is costly,
  naming a function for `ID` (−39% to −63%, half the bytes). Slower where reflect is cheap: `Mount` +9–15%, `URLFor`
  +6–19%, `isComponent` +63%, because each table read is a map lookup keyed by `reflect.Type` where `reflect` reads a
  field.
- **TinyGo vs reflect: about 3.4× the time and 2–3× the bytes.** The tables cost about the same as `reflect` on Go, so
  this gap is TinyGo's compiler, runtime and garbage collector, not structpages. A simple GET is 2.5µs. These TinyGo
  runs are native binaries, not WebAssembly.

## Strategy

In order:

1. **Make routing run under TinyGo.** It is most of the gap and it is what a Workers deploy depends on. Today
   structpages only tells TinyGo users to bring a mux that understands Go 1.22 patterns, and its tests skip instead of
   using one, so routing is never exercised under TinyGo. Give the test suite such a mux in TinyGo builds, so the 11
   routing tests run instead of skipping. An earlier attempt copied Go's `ServeMux` into the library and was reverted
   the same day; a copy used only by the tests keeps the library unchanged. In parallel, follow
   [tinygo-org/net#56](https://github.com/tinygo-org/net/pull/56), which fixes method patterns but not escaped
   slashes.
2. **Prove it end to end.** Nothing yet builds an app with TinyGo for WebAssembly. Build one example (`simple`) to
   `wasm`, run it, and record its binary size and start-up time. For Workers those matter more than microseconds per
   request, and no number here measures them yet.
3. **Keep the fork small.** The branch changes 33 of upstream's files, so every merge from upstream risks conflicts.
   Most of those changes are mechanical: calls into `reflect` replaced by the wrapper functions, which change nothing
   on standard Go. Offering that indirection upstream would shrink the fork to the tables, the generator and the
   TinyGo docs. That is a conversation to have with the maintainer before sending anything; nothing has gone upstream.
4. **Accept the documented limits.** Generic page types and function literals stay unsupported in tables builds; the
   generator warns about them and `docs/tinygo.md` says so.
5. **Speed up the tables only if a profile asks for it.** The generator knows every page type, so it could write
   direct calls and cache each method's table instead of looking it up per call. On TinyGo the cost is TinyGo itself,
   so this would mostly help standard Go users who opt into the tables, who don't need them.
6. **Delete the tables when TinyGo catches up.** Once TinyGo's `reflect` can list and call methods
   ([tinygo#3862](https://github.com/tinygo-org/tinygo/issues/3862)) and name functions
   ([tinygo#4379](https://github.com/tinygo-org/tinygo/issues/4379)), the comment at the top of `reflect_tables.go`
   lists what to remove. `mise run parity` with no generated tables is the check.

## Risks to keep in mind

- `tables/key_tinygo.go` reads TinyGo's internal layout of a function value through `unsafe`. A TinyGo release that
  changes it breaks `ID` silently. The TinyGo test run would catch it, so run it on every TinyGo upgrade.
- A request-arg provider declared with a named function type (`type P func(*http.Request) (T, error)`) probably gets
  no table, so it would work on Go and fail at `Mount` on TinyGo. Untested.
- Found upstream, not caused here and left alone: `tools/lint`'s `TestRegion_Integration` fails on upstream `main`;
  `mise run lint:examples` reports three hard-coded URLs in `examples/blog`; the README says MIT where `LICENSE` is
  BSD 3-Clause.

## Checking progress

```bash
mise run parity          # what still differs between Go and TinyGo
mise run test:tinygo     # the suite under TinyGo
mise run bench:compare   # reflect vs tables vs TinyGo, written to BENCH-COMPARE.GENERATED.md
mise run ci:go           # everything upstream's CI checks on standard Go
```
