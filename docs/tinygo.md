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
go tool structpages generate -tags tinygo ./...
tinygo test ./...
```

Routing needs nothing from you: TinyGo's `net/http` comes from [tinygo-org/net](https://github.com/tinygo-org/net),
whose `ServeMux` still routes like Go 1.21's ([net#55](https://github.com/tinygo-org/net/issues/55); a fix waits in
[net#56](https://github.com/tinygo-org/net/pull/56)), so in a TinyGo build `Mount` routes through its own copy of Go's
(see [Generated code: Routing](./generated.md#routing)).

## Serving as a Worker

A WebAssembly build can't listen on a socket: in a Worker, workerd (Cloudflare's runtime, locally) or Cloudflare
delivers the requests. `github.com/jackielii/structpages/workers` serves the same handler both ways:

```go
mux := http.NewServeMux()
structpages.Mount(mux, index{}, "/", "App")
log.Fatal(workers.Serve(":8080", mux))
```

On the standard toolchain `workers.Serve` is `ListenAndServe`. In a Worker it hands the app every request, one Go
runtime serving them all; workers-go's own `Serve` starts a new one per request, keeping nothing between them. The
Worker also needs JavaScript glue and a workerd config next to its `app.wasm`:

```bash
go run github.com/syumai/workers-go/cmd/workers-assets-gen -mode tinygo -o build
go run github.com/jackielii/structpages/tools/workers/cmd/structpages-workers -o build
tinygo build -o build/app.wasm -target wasm -no-debug .
workerd serve build/config.capnp
```

`structpages-workers` writes a `worker.mjs`, over the one `workers-assets-gen` writes, that starts one Go runtime per
isolate, and a `config.capnp` serving on `:8080`. In this repo, `mise run example:todo:serve:static-worker` does all of that
for `examples/todo`, in a temporary folder it deletes on Ctrl-C.

## When TinyGo catches up

Two things exist for TinyGo's gaps, each with a comment saying how to remove it:

- **The generated code** (`methods_static.go`, package `generated`, `structpages generate`): TinyGo's `reflect` can't
  list or call methods ([tinygo#3862](https://github.com/tinygo-org/tinygo/issues/3862)) and `runtime.FuncForPC`
  returns nil ([tinygo#4379](https://github.com/tinygo-org/tinygo/issues/4379)). Once a TinyGo release has both,
  TinyGo builds no longer need it; whether the standard toolchain's static build stays is a choice of its own.
- **The routing** (`routes_static.go`, `internal/servemux`): once TinyGo's `net/http` routes Go 1.22 patterns,
  `Mount` can register on the app's mux in every build again.
