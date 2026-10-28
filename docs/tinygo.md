---
title: TinyGo
slug: /tinygo
sidebar_position: 15
---

# TinyGo

structpages builds with [TinyGo](https://tinygo.org), whose smaller WebAssembly suits hosts such as Cloudflare
Workers. TinyGo's `reflect` can't list or call a type's methods, read a function's parameters, or name a function,
so a TinyGo build reads [generated code](./static.md) instead. Generate it first, with `-tags tinygo` when some
files build only under TinyGo, or never under it:

```bash
go tool structpages-static generate -tags tinygo ./...
tinygo test ./...
```

Routing needs nothing from you: TinyGo's `net/http` comes from [tinygo-org/net](https://github.com/tinygo-org/net),
whose `ServeMux` still routes like Go 1.21's ([net#55](https://github.com/tinygo-org/net/issues/55); a fix waits in
[net#56](https://github.com/tinygo-org/net/pull/56)), so in a TinyGo build `Mount` routes through its own copy of Go's
(see [Static builds: Routing](./static.md#routing)).

## Serving as a Worker

A WebAssembly build can't listen on a socket: in a Worker, workerd (Cloudflare's runtime, locally) or Cloudflare
delivers the requests. So the app keeps its ordinary `main` for every other build, and adds a second one, in a file
built only for the Worker, that serves the same handler through `github.com/jackielii/structpages/workers`:

```go
//go:build js && wasm

package main

func main() { workers.Serve(handler()) }
```

Its ordinary `main` goes in a file built `!(js && wasm)`, with `http.ListenAndServe(":8080", handler())`, as
`examples/todo`'s `main_server.go` and `main_worker.go` do. In a Worker `workers.Serve` takes the app's requests from
the glue `structpages-workers` writes, `worker.mjs`, and keeps its Go runtime for the next one; workers-go's own
`Serve` starts a new runtime per request, keeping nothing between them. The Worker also needs that glue and a workerd
config next to its `app.wasm`. `structpages-workers` builds all of it, in the app's folder:

```bash
go get -tool github.com/jackielii/structpages/tools/workers/cmd/structpages-workers@latest
go tool structpages-workers tinygo -o build/tinygo   # once: TinyGo, patched (below)
go tool structpages-workers                          # the Worker, into build/, with build/tinygo
workerd serve build/config.capnp
```

In order, it writes workers-go's glue (`wasm_exec.js` and `runtime.mjs`, from `workers-assets-gen` at the version your
`go.mod` requires), then its own `worker.mjs` over workers-go's, and a `config.capnp` serving on `:8080`, then
`app.wasm` (`tinygo build -target wasm -no-debug`, with the flags below), then `build.txt`, which says how it was
built. In this repo, `mise run example:todo:serve:static-worker` builds `examples/todo` that way into
`build/static-worker/` and serves it.

### Every trick, on by default

`structpages-workers`' defaults are the fastest Worker it knows how to build, so the plain command is the one to use.
Each trick is a flag that turns it off, and `examples/todo` builds its Worker once with each turned off, and once with
none, as TinyGo and workers-go ship: [its BENCH_GENERATED.md](../examples/todo/BENCH_GENERATED.md) shows what each
saves.

- **The glue, `worker.mjs`**, with `workers.Serve` its Go side (`-glue workers-go` writes the glue it replaced):
  - Go runtimes kept between requests: a pool of up to four, a runtime serving one request at a time and kept only
    when its answer ended normally and Go says its heap has room for another.
  - Two runtimes started while the module loads, each answering a GET of `-warm`'s paths (default `/`) into nothing,
    so a new isolate's first request finds one ready, its code already run once.
  - A request goes into Go, and its answer out, as numbers: the glue writes the request's method, URL, headers and
    body into the runtime's memory and calls Go's exported `structpages_serve` with their lengths; Go answers through
    an imported `respond` with where its answer's bytes are, and the glue copies them out. No JavaScript value is
    made for either, where workers-go's own request path crosses between JavaScript and Go some hundred times, and
    even one `js.FuncOf` call a dozen, each with a finalizer.
  - TinyGo's sleeps rounded up to a whole millisecond, as Cloudflare's clock moves only by whole milliseconds
    ([tinygo#5798](https://github.com/tinygo-org/tinygo/issues/5798)), and TinyGo's start-up seed taken from
    `Math.random` while the module loads, when Cloudflare gives no crypto randomness.
  - A request with the header `x-go-runtime` is told which kind of runtime answered it: new, warm or reused.
- **The leaking collector** (`-gc leaking`; `-gc precise` is TinyGo's own): memory is never collected, so allocating
  is a pointer moving on, and no request ever waits for a collection. The glue gives a runtime no more requests once
  its heap is used up (Go tells it with each answer), and a fresh one takes over. workers-go's glue keeps one runtime
  for good, so it needs a collector: the build refuses `-glue workers-go` with the leaking one. A handler that streams
  for a long time keeps what it allocates until its stream ends, and an app whose requests each allocate megabytes
  would start a runtime for most of them: build those with `-gc precise`.
- **TinyGo's fastest code** (`-opt 2`; `-opt z` is its smallest, larger in time and smaller in bytes).
- **A patched TinyGo** (`structpages-workers tinygo -o build/tinygo`; the build uses `-tinygoroot`, default
  `build/tinygo`, and `-tinygoroot installed` is TinyGo as it ships). The copy has three changes to TinyGo's runtime,
  each explained where `structpages-workers` lists them; your install isn't touched:
  - No collection at every await: TinyGo's runtime runs a full collection whenever its scheduler goes idle once 32
    finalizers were registered, and every JavaScript value Go holds registers one
    ([tinygo#5800](https://github.com/tinygo-org/tinygo/issues/5800)). It matters under TinyGo's own collector.
  - Goroutine stacks reused: every call into Go starts a goroutine, whose stack TinyGo allocates and clears, and an
    exported function's goroutine is never freed. The copy keeps finished goroutines' stacks for new ones.
  - `sync.Once` without a lock once done, as Go's: gsx's HTML escaping calls one for each string it writes.

  In this repo, mise's `tools:tinygo` task makes it, in `build/tinygo`, and every Worker build depends on it.
- **`-heap`**: the WebAssembly's starting heap, in MB (default 8; `-heap 0` is TinyGo's own), room for a runtime's
  requests without growing its memory.
- **`-stack`**: TinyGo's stack per goroutine (default 64kb, TinyGo's own; with stacks reused, its size costs nothing
  per request).

structpages itself helps every way, not only the Worker: `URLFor` and `ID` work out a page type's route and a
method's id once, from the page tree, which doesn't change after `Mount`, where they walked the tree on every call.

## When TinyGo catches up

Two things exist for TinyGo's gaps, each with a comment saying how to remove it:

- **The generated code** (`methods_static.go`, package `static`, `structpages-static generate`): TinyGo's `reflect` can't
  list or call methods ([tinygo#3862](https://github.com/tinygo-org/tinygo/issues/3862)) and `runtime.FuncForPC`
  returns nil ([tinygo#4379](https://github.com/tinygo-org/tinygo/issues/4379)). Once a TinyGo release has both,
  TinyGo builds no longer need it; whether the standard toolchain's static build stays is a choice of its own.
- **The routing** (`routes_static.go`, `internal/servemux`): once TinyGo's `net/http` routes Go 1.22 patterns,
  `Mount` can register on the app's mux in every build again.
