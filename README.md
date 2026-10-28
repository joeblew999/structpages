# structpages

[![CI](https://github.com/jackielii/structpages/actions/workflows/ci.yml/badge.svg)](https://github.com/jackielii/structpages/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/jackielii/structpages.svg)](https://pkg.go.dev/github.com/jackielii/structpages)
[![codecov](https://codecov.io/gh/jackielii/structpages/branch/main/graph/badge.svg)](https://codecov.io/gh/jackielii/structpages)
[![Go Report Card](https://goreportcard.com/badge/github.com/jackielii/structpages)](https://goreportcard.com/report/github.com/jackielii/structpages)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)

**Documentation:** <https://jackielii.github.io/structpages/>

Struct Pages provides a way to define routing using struct tags and methods. It integrates with Go's `http.ServeMux`, allowing you to quickly build web applications with minimal boilerplate.

**Status**: **Beta** - The API has settled and the library is battle-tested in production, carrying medium-to-large applications. Remaining breaking changes before v1 will be rare and called out in release notes.

## Features

- **Struct-based routing** - Define routes using struct tags
- **gsx-first** - Examples and docs use [gsx](https://gsxhq.github.io/); any value with `Render(ctx, w)` renders, so [templ](https://templ.guide/) and `html/template` work too
- **HTMX-friendly** - With `WithTargetSelector(structpages.HTMXRenderTarget)` (or `HTMXv4RenderTarget`), an htmx request renders just the component its `HX-Target` names
- **Middleware** - Standard Go middleware pattern
- **Type-safe URLs** - Generate URLs from struct references
- **Dependency injection** - Pass dependencies to handlers via options

## Installation

```shell
go get github.com/jackielii/structpages
```

## Quick Start

Define your page structure using struct tags, and render pages with gsx. In `pages.gsx`:

```gsx
package main

type index struct {
	product `route:"/product Product"`
	team    `route:"/team Team"`
}

type product struct{}
type team struct{}

// Page renders the index page. `|> url` resolves a page to its URL.
component (p index) Page() {
	<html>
		<body>
			<h1>Welcome</h1>
			<nav>
				<a href={product{} |> url}>Product</a>
				<a href={team{} |> url}>Team</a>
			</nav>
		</body>
	</html>
}

component (p product) Page() {
	<h1>Product</h1>
}

component (p team) Page() {
	<h1>Team</h1>
}
```

In `main.go`:

```go
package main

import (
    "log"
    "net/http"

    "github.com/jackielii/structpages"
)

func main() {
    mux := http.NewServeMux()
    if _, err := structpages.Mount(mux, index{}, "/", "Home"); err != nil {
        log.Fatal(err)
    }
    log.Println("Starting server on :8080")
    log.Fatal(http.ListenAndServe(":8080", mux))
}
```

The `url` filter comes from a `gsx.toml` at the module root that registers structpages' helpers:

```toml
[filters]
url    = "github.com/jackielii/structpages.URLFor"
id     = "github.com/jackielii/structpages.ID"
target = "github.com/jackielii/structpages.IDTarget"
```

Generate and run with `go tool gsx generate . && go run .` (pin the CLI with
`go get -tool github.com/gsxhq/gsx/cmd/gsx@latest`). The [Quick Start guide](./docs/quick-start.md) walks
through it step by step.

## Documentation

The full docs live at <https://jackielii.github.io/structpages/>. Good starting points:

- [Quick Start](https://jackielii.github.io/structpages/quick-start) and [Concepts](https://jackielii.github.io/structpages/concepts)
- [Routing](https://jackielii.github.io/structpages/routing): the `route:"[method] path [Title]"` tag, path params, nesting
- [gsx Patterns](https://jackielii.github.io/structpages/gsx) and [HTMX Integration](https://jackielii.github.io/structpages/htmx): layouts, the Props pattern, partial rendering
- [How a Request Is Rendered](https://jackielii.github.io/structpages/rendering): the target selector, Props, and which component renders
- [URLFor & ID](https://jackielii.github.io/structpages/urlfor): type-safe URLs and element ids
- [Dependency Injection](https://jackielii.github.io/structpages/dependency-injection), [Error Handling](https://jackielii.github.io/structpages/error-handling), [Middleware](https://jackielii.github.io/structpages/middleware)
- [Testing](https://jackielii.github.io/structpages/testing) and [Lint](https://jackielii.github.io/structpages/lint)
- [Generated code](./docs/static.md): running without method reflection, on the standard toolchain or under TinyGo
- [TinyGo](https://jackielii.github.io/structpages/tinygo): building for WebAssembly hosts such as Cloudflare Workers
- [API Guide](https://jackielii.github.io/structpages/api)

## Examples

Runnable apps live in [`examples/`](./examples); [examples/README.md](./examples/README.md) lists them in the order to read them.

## Lint Tool

`structpages-lint` checks every `URLFor`/`ID`/`IDTarget`/`Within`/`Ref` call, `url`/`id`/`target` filter and hard-coded internal URL against the page tree rebuilt from your route tags, so a renamed route fails CI instead of becoming a 404. It also flags a `Mount` with no `WithTargetSelector` when its pages' templates use htmx:

```shell
go get -tool github.com/jackielii/structpages/tools/lint/cmd/structpages-lint@latest
go tool structpages-lint ./...
```

Categories, suppression and CI setup: [Lint](https://jackielii.github.io/structpages/lint).

## Claude Code Skill

This repo ships a [Claude Code](https://claude.com/claude-code) plugin that teaches Claude the structpages idioms (`Props`/`RenderTarget`, HTMX partials, `URLFor`/`ID`/`IDTarget`, middleware, DI). Inside Claude Code:

```text
/plugin marketplace add jackielii/structpages
/plugin install structpages@structpages
```

Restart Claude Code when prompted. The skill loads automatically when you work on a structpages project; you can also invoke it explicitly with `/structpages:structpages`.

## Contributing

See [CONTRIBUTING.md](./CONTRIBUTING.md) for development setup and guidelines.

## License

MIT License - see [LICENSE](./LICENSE) file for details.

## TinyGo

This fork's `tinygo` branch makes structpages build with [TinyGo](https://tinygo.org), so the same app that runs on
standard Go can compile to small WebAssembly for hosts such as Cloudflare Workers.

**Why it needs work.** structpages uses reflection to find a page's methods, call them, fill their parameters, and
name the method passed to `ID`. TinyGo's `reflect` can't list or call methods
([tinygo#3862](https://github.com/tinygo-org/tinygo/issues/3862)), and its `runtime.FuncForPC` returns nil
([tinygo#4379](https://github.com/tinygo-org/tinygo/issues/4379)).

**What we do.** Everything structpages reads about methods and functions goes through one small set of functions with
two implementations, picked by build tag:

| Build | Implementation | Methods come from |
|---|---|---|
| standard Go (default) | `methods_dynamic.go` | `reflect`, at run time, as before: nothing changes for Go users |
| TinyGo, or Go with `-tags structpages_static` | `methods_static.go` | code generated ahead of time by `tools/static` |

So structpages runs three ways: **dynamic** (standard Go, the default: methods found at run time with `reflect`),
**static** (standard Go, methods known at compile time from the generated code) and **static-worker** (a Cloudflare
Worker: TinyGo WebAssembly, static too). The same tests and benchmarks run all three ways, and since the static build also runs
on standard Go, a problem in the generated code can be told apart from a TinyGo one. The goal is parity, and the
library's tests now pass all three ways with nothing skipped: under TinyGo `Mount` routes through its own copy of Go's
`ServeMux`, since TinyGo's can't route Go 1.22 patterns. What generated code can't describe is a function literal
passed to `ID`. [docs/static.md](./docs/static.md) says how to use it, and [docs/tinygo.md](./docs/tinygo.md) how to
build for TinyGo and Workers, and what to remove once TinyGo catches up.

**Running it.** Install [mise](https://mise.jdx.dev), which brings the Go, TinyGo and tool versions in `mise.toml`;
`mise tasks` lists everything. Every step is a task of its own, depending on the one before, so running any task runs
the steps it needs first, in order: for an example, `generate:gsx` (`.x.go` from the templates), then
`generate:structpages-static` (`structpages_static.generated.go` from the Go code), then `generate:e2e` (its recorded
answers), then `test`, `build`, `serve` and `bench`, each way. The library has the same, less what only an app has.
Each folder's section of `mise.toml` sets its steps out; the examples' generated files are committed, as upstream
commits `.x.go`. Where a step needs more than one plain command, `structpages-dev` (`tools/dev`, which mise builds)
does that one step: a test summary, a benchmark, a report or a server.

| Task | Runs |
|---|---|
| `mise run test` / `test:dynamic` / `test:static` / `test:static-worker` | the library's tests, all three ways or one, a line each with every test skipped and why |
| `mise run example:todo:test` / `example:todo:test:dynamic` / … | an example's tests, all its ways or one |
| `mise run example:todo:serve:dynamic` / `example:todo:serve:static` / `example:todo:serve:static-worker` | serve an example's build on http://localhost:8080 (static-worker: WebAssembly in workerd) |
| `mise run bench` / `example:todo:bench` | benchmarks, each way one at a time: in-process, and for an app served (the time and the server's CPU per request) and started; todo's also compares its Worker built without each of `structpages-workers`' switches. Into that folder's `BENCH_GENERATED.md` ([the library's](./BENCH_GENERATED.md), [todo's](./examples/todo/BENCH_GENERATED.md)) |
| `mise run examples` / `bench:examples` | every example's tests / benchmarks |
| `mise run generated:check` | fail if a generated file changed and isn't committed |
| `mise run lint:examples` | structpages-lint (`tools/lint`) over every example |
| `mise run ci:go` | everything CI checks on standard Go |

Two examples run fewer ways, and their sections say why: html-template has no static-worker (Go's `html/template` uses
parts of `reflect` TinyGo lacks), and lint-misuse is a fixture for the linter, not an app to serve.

On standard Go, generated code comes out about even with `reflect` in the library's micro-benchmarks (faster for `ID`,
slower for `Mount` and `URLFor`), and 23% faster serving `todo`'s page, with a fifth fewer allocations.

Where this stands and what comes next: [TINYGO-STRATEGY.md](./TINYGO-STRATEGY.md).
