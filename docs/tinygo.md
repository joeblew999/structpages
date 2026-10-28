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
the glue `structpages-workers` writes, `worker.mjs`, and keeps its Go runtime alive for the next one; workers-go's own
`Serve` starts a new runtime per request, keeping nothing between them. The Worker also needs that glue and a workerd
config next to its `app.wasm`. `structpages-workers` builds all of it, in the app's folder:

```bash
go get -tool github.com/jackielii/structpages/tools/workers/cmd/structpages-workers@latest
go tool structpages-workers tinygo -o build/tinygo      # once: TinyGo, patched (below)
go tool structpages-workers -tinygoroot build/tinygo    # the Worker, into build/
workerd serve build/config.capnp
```

In order, it writes workers-go's glue (`wasm_exec.js` and `runtime.mjs`, from `workers-assets-gen` at the version your
`go.mod` requires), then its own `worker.mjs` over workers-go's, and a `config.capnp` serving on `:8080`, then
`app.wasm` (`tinygo build -target wasm -no-debug -opt=z`: the smallest code, and the quickest build). In this repo,
`mise run example:todo:serve:static-worker` builds `examples/todo` that way into `build/static-worker/` and serves it.

What makes a request cheap, each measured on its own in
[examples/todo's BENCH_GENERATED.md](../examples/todo/BENCH_GENERATED.md):

- **The glue, `worker.mjs`**, with `workers.Serve` its Go side:
  - Go runtimes kept between requests: a pool of up to four, a runtime serving one request at a time and kept only
    when its answer ended normally and Go says its heap has room for another.
  - Two runtimes started while the module loads, each answering a GET of `-warm`'s paths (default `/`) into nothing,
    so a new isolate's first request finds one ready.
  - One crossing into Go and one out: the request as two values (its method, URL and headers as lines, and its body),
    the answer in one call. workers-go's own request path crosses some hundred times.
  - TinyGo's sleeps rounded up to a whole millisecond, as Cloudflare's clock moves only by whole milliseconds
    ([tinygo#5798](https://github.com/tinygo-org/tinygo/issues/5798)), and TinyGo's start-up seed taken from
    `Math.random` while the module loads, when Cloudflare gives no crypto randomness.
  - A request with the header `x-go-runtime` is told which kind of runtime answered it: new, warm or reused.

  `-glue workers-go` writes the glue it replaced, to measure what it saves.

- **A patched TinyGo** (`structpages-workers tinygo`, then `-tinygoroot`): TinyGo's runtime runs a full garbage
  collection whenever its scheduler goes idle once 32 finalizers were registered. Every JavaScript value Go holds
  registers one, and a Worker goes idle at every await, so it collects on every request
  ([tinygo#5800](https://github.com/tinygo-org/tinygo/issues/5800)). `structpages-workers tinygo` makes a copy of the
  TinyGo installed with that threshold off (the collector still runs when the heap is full); your install isn't
  touched. On `examples/todo` in workerd it cuts the CPU per request by about 80%
  ([its BENCH_GENERATED.md](../examples/todo/BENCH_GENERATED.md)). Without `-tinygoroot`, the build uses the TinyGo
  installed. In this repo, mise's `tools:tinygo` task makes it, in `build/tinygo`, and every Worker build depends on
  it.
- **`-heap`**: the WebAssembly's starting heap, in MB (default 8; `-heap 0` is TinyGo's own), so an ordinary request
  doesn't need a collection to find room.

`build/build.txt` says which a build used.

## When TinyGo catches up

Two things exist for TinyGo's gaps, each with a comment saying how to remove it:

- **The generated code** (`methods_static.go`, package `static`, `structpages-static generate`): TinyGo's `reflect` can't
  list or call methods ([tinygo#3862](https://github.com/tinygo-org/tinygo/issues/3862)) and `runtime.FuncForPC`
  returns nil ([tinygo#4379](https://github.com/tinygo-org/tinygo/issues/4379)). Once a TinyGo release has both,
  TinyGo builds no longer need it; whether the standard toolchain's static build stays is a choice of its own.
- **The routing** (`routes_static.go`, `internal/servemux`): once TinyGo's `net/http` routes Go 1.22 patterns,
  `Mount` can register on the app's mux in every build again.
