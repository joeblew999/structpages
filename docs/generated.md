---
title: Generated code
slug: /generated
sidebar_position: 14
---

# Generated code

structpages finds a page's methods, fills their parameters and resolves `ID(ctx, p.Content)` through method
reflection. It can read the same from code generated ahead of time instead, so it runs three ways:

| Way | Build | Methods come from |
|---|---|---|
| reflect | standard Go (the default) | `reflect` |
| generated | standard Go, `-tags structpages_generated` | generated code |
| TinyGo | TinyGo ([TinyGo](./tinygo.md)) | generated code: TinyGo's `reflect` can't list or call methods or name a function |

Your pages don't change. On the standard toolchain the generated build is a choice: no method reflection, and on a
real page it is faster (in this repo, `examples/todo`'s page renders about a fifth faster than with `reflect`). Under
TinyGo it is the only way.

## Generate the code

```bash
go get -tool github.com/jackielii/structpages/tools/gen/cmd/structpages-gen@latest
go tool structpages-gen ./...
```

It writes `structpages.generated.go` into each package that declares pages, standalone components or request-arg
providers, next to them: generated code can only name a package's unexported types from inside that package. Only
the generated builds compile it (`//go:build tinygo || structpages_generated`), so the reflect build ignores it.

- Commit it, as you would gsx's `.x.go`, so every build works from a fresh checkout, and have CI regenerate it and
  fail if anything changed (this repository's examples do both). Or generate it in your build before compiling.
  Either way, run the generator again whenever a page's methods change.
- Generate in order, each step reading what the one before wrote: `go tool gsx generate` first in gsx projects
  (page components are methods in the generated `.x.go`), then structpages-gen, then record an end-to-end test
  (the `e2e` package) if you have one.
- Missing generated code fails at `Mount`, naming the type: `no generated methods for app.page; run go tool
  structpages-gen`. Code generated before a method was added doesn't fail there: the method isn't seen, and the
  page fails when it uses it. An end-to-end test run the generated way catches that.
- If some files build only under TinyGo, or never under it, pass `-tags tinygo` so the code matches what TinyGo
  compiles.
- `-test` also writes `structpages.generated_test.go` for what your `_test.go` files declare.

## Build and test with it

```bash
go build -tags structpages_generated ./...
go test -tags structpages_generated ./...
```

The test is the quickest way to find code the generator missed, without TinyGo.

## Routing

In both generated builds `Mount` routes its pages through its own copy of Go 1.26's `ServeMux` (`internal/servemux`):
TinyGo's `net/http` can't route Go 1.22 patterns, and the standard toolchain's generated build does the same so that
the two differ only in their compiler. Give `Mount` any mux, `http.NewServeMux()` included.

Your mux then sees only the mount's prefix (`/`, or `/admin` and `/admin/`), so one difference from the reflect
build: a handler of your own on the same mux that is more specific than that prefix but less specific than a page's
route takes the page's requests (your `/admin/` over a page's `/admin/settings`, mounted at `/`), and a route your mux
would reject as a conflict isn't one. Mount on a mux of its own, or keep your handlers outside the mount's paths.

## What generated code can't do

- **Function literals as references.** `ID`, `IDTarget`, `RenderTarget.Is` and the gsx `id` and `target` filters
  name declared functions and methods, as method values (`p.Content`) or expressions (`page.Content`), not a
  closure: the generated builds return an error for one.
- **Generic page types used only inside generic code.** The generator writes code for each instantiation your code
  uses (`listPage[string]` in a route field), and warns about a generic page type it finds none of.
- **Providers the generator never saw.** A request-arg provider is found where your code mentions it; a function
  type that only appears at run time fails at `Mount`.
