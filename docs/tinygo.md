---
title: TinyGo
slug: /tinygo
sidebar_position: 14
---

# TinyGo

structpages builds with [TinyGo](https://tinygo.org), whose smaller and faster WebAssembly suits hosts such as
Cloudflare Workers. TinyGo's `reflect` can't list or call a type's methods, read a function's parameters, or name a
function, and that is how structpages finds a page's methods, fills their parameters, and resolves
`ID(ctx, p.Content)`. A TinyGo build reads the same from tables generated ahead of time. Your pages don't change.

## Generate the tables

```bash
go get -tool github.com/jackielii/structpages/cmd/structpages-gen@latest
go tool structpages-gen ./...
```

It writes `structpages_tables.go` into each package that declares pages, standalone components or request-arg
providers. Only TinyGo builds the file (`//go:build tinygo || structpages_tables`), so the standard toolchain
ignores it.

- Commit it, as you would gsx's `.x.go`, or generate it in your build before compiling. Either way, run the
  generator again whenever a page's methods change. In gsx projects run it after `go tool gsx generate`: page
  components are methods in the generated `.x.go`.
- A stale table fails at `Mount`, naming the type: `no method table for app.page; run go tool structpages-gen`.
- If some files build only under TinyGo, or never under it, pass `-tags tinygo` so the tables match what TinyGo
  compiles.
- `-test` also writes tables for what your `_test.go` files declare, for `tinygo test`.

## Check without TinyGo

```bash
go test -tags structpages_tables ./...
```

runs your tests on the standard toolchain with structpages reading the tables instead of `reflect`: the quickest
way to find a missing table.

## What the tables can't do

- **Generic page types.** The generator reports them and leaves them out, so mounting one fails in a TinyGo build.
- **Function literals as references.** `ID`, `IDTarget`, `RenderTarget.Is` and the gsx `id` and `target` filters
  name declared functions and methods, as method values (`p.Content`) or expressions (`page.Content`), not a
  closure.
- **Providers the generator never saw.** A request-arg provider is found where your code mentions it; a function
  type that only appears at run time fails at `Mount`.

## Routing

TinyGo's `net/http` comes from [tinygo-org/net](https://github.com/tinygo-org/net), whose `ServeMux` still routes
like Go 1.21's: a pattern with a method (`POST /add`), a wildcard (`{id}`) or `{$}` never matches
([net#55](https://github.com/tinygo-org/net/issues/55); a fix waits in [net#56](https://github.com/tinygo-org/net/pull/56)).
structpages registers Go 1.22 patterns, so in a TinyGo build give `Mount` a mux that understands them: anything with
`Handle(pattern string, handler http.Handler)`, such as a copy of Go's own `ServeMux`.

## When TinyGo catches up

Two things exist only for TinyGo's gaps, each with a comment saying how to remove it:

- **The tables** (`reflect_tables.go`, package `tables`, `structpages-gen`): TinyGo's `reflect` can't list or call
  methods ([tinygo#3862](https://github.com/tinygo-org/tinygo/issues/3862)) and `runtime.FuncForPC` returns nil
  ([tinygo#4379](https://github.com/tinygo-org/tinygo/issues/4379)). Once a TinyGo release has both, delete them
  and the build constraints on `reflect_runtime.go`; the suite must pass under `tinygo test` with no generated
  tables.
- **The benchmarks' `//go:build !tinygo`**: TinyGo 0.43's compiler crashes building `struct_pages_bench_test.go`,
  whose local `index` types clash with a package-level one (the file says how). Drop the constraint once `tinygo test`
  builds it.
