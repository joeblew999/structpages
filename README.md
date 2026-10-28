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
- [Generated code](./docs/generated.md): running without method reflection, on the standard toolchain or under TinyGo
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
| standard Go (default) | `reflect_runtime.go` | `reflect`, as before: nothing changes for Go users |
| TinyGo, or Go with `-tags structpages_generated` | `reflect_generated.go` | code generated ahead of time by `tools/gen` |

So structpages runs three ways: reflect, generated (standard Go without method reflection) and TinyGo (which always
reads the generated code). The same tests and benchmarks run all three ways, and since the generated build also runs
on standard Go, a problem in the generated code can be told apart from a TinyGo one. The goal is parity, and the
library's tests now pass all three ways with nothing skipped: under TinyGo `Mount` routes through its own copy of Go's
`ServeMux`, since TinyGo's can't route Go 1.22 patterns. What generated code can't describe is a function literal
passed to `ID`. [docs/generated.md](./docs/generated.md) says how to use it, and [docs/tinygo.md](./docs/tinygo.md) how to
build for TinyGo and Workers, and what to remove once TinyGo catches up.

**Running it.** Install [mise](https://mise.jdx.dev), which brings the Go, TinyGo and tool versions in `mise.toml`;
`mise tasks` lists everything. The tasks run `structpages-dev` (`tools/dev`) on one folder, the library or an example,
with the same steps every way: generate the code the way needs, build, test, benchmark or serve, then delete what it
generated. A task over several folders lists them in `mise.toml`.

| Task | Runs |
|---|---|
| `mise run test` | the library's tests three ways (reflect, generated, tinygo), with every test each skips |
| `mise run examples` | every example's tests three ways, a line each (`examples:record` re-records `todo`'s e2e test) |
| `mise run example:todo:serve:reflect` / `example:todo:serve:generated` / `example:todo:serve:tinygo` | serve an example on http://localhost:8080 each way (tinygo: WebAssembly in workerd; `todo` so far) |
| `mise run example:todo:test` / `example:todo:bench` | `todo`'s tests three ways / its benchmarks three ways, into [examples/todo/BENCH_GENERATED.md](./examples/todo/BENCH_GENERATED.md) |
| `mise run bench` / `bench:examples` | benchmarks three ways: the library's into [BENCH_GENERATED.md](./BENCH_GENERATED.md), each example's into its own `BENCH_GENERATED.md` ([todo's](./examples/todo/BENCH_GENERATED.md)) |
| `mise run gen:examples` / `lint:examples` | structpages-gen (`tools/gen`) or structpages-lint (`tools/lint`) over every example |
| `mise run clean:generated` | delete everything generated, if a run was stopped before it could |
| `mise run ci:go` | everything CI checks on standard Go |

On standard Go, generated code comes out about even with `reflect` in the library's micro-benchmarks (faster for `ID`,
slower for `Mount` and `URLFor`), and 23% faster serving `todo`'s page, with a fifth fewer allocations.

Where this stands and what comes next: [TINYGO-STRATEGY.md](./TINYGO-STRATEGY.md).
