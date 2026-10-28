# Performance Optimizations

This document tracks performance optimizations and future improvement opportunities.

## Implemented Optimizations (v0.x.x - 2025-10-31)

### 1. Cache extractMethodInfo Results
- **Impact:** 80-89% faster reflection operations
- **Implementation:** Global cache for method info, by `funcKey`: a `map[uintptr]` under an RWMutex (a `sync.Map`
  keyed by `any` cost TinyGo a reflect-based hash and comparison per lookup)
- **Benefit:** Zero allocations for unbound method lookups

### 2. Cache parseSegments Results
- **Impact:** 10-17% faster URL generation
- **Implementation:** Per-context cache with RWMutex
- **Benefit:** Eliminated redundant pattern parsing

### 3. Pre-parse Route Segments
- **Impact:** 3% faster request handling with params
- **Implementation:** Store parsed segments in PageNode at Mount time
- **Benefit:** Zero parsing overhead in extractURLParams middleware

### 4. Work URLFor's Route and ID's Id Out Once (2026-10-28)
- **Impact:** `examples/todo`'s page about 2× faster in-process (9.7µs to 4.7µs with `reflect`, 7.5µs to 4.7µs
  generated), as it asks for two URLs and two ids per item
- **Implementation:** per-context caches, filled on first use: a typed page value's route pattern, by its type, and
  a method's id, by its method info, the page being served and the form (`parseContext.routes`, `.ids`). The page
  tree no longer changes once Mount has built it, so neither needs invalidating.
- **Also:** URLFor's key/value arguments are matched without building maps, and its values formatted without
  `fmt` for strings and integers (`formatArg`)

## Benchmarks

Run benchmarks to verify performance:

```bash
# Full benchmark suite
go test -bench=. -benchmem

# Specific categories
go test -bench=BenchmarkRequestHandling -benchmem
go test -bench=BenchmarkURLGeneration -benchmem
go test -bench=BenchmarkIDGeneration -benchmem
go test -bench=BenchmarkReflection -benchmem
```

## Future Optimization Opportunities

### Medium Priority

1. **Pool availableArgs Maps**
   - Use `sync.Pool` for argument maps in method calling
   - Estimated improvement: 5-10% in request handling

2. **Optimize kebabToPascal**
   - Single-pass implementation without `strings.Split`
   - Estimated improvement: 50% in HTMX target matching

### Low Priority

3. **Cache Component Results**
   - For pure components with no side effects
   - Requires careful API design

4. **Reduce Context Value() Calls**
   - Extract once at handler start
   - Pass as struct instead of multiple lookups

## Performance Characteristics

Current performance (Apple M3 Ultra):
- Simple GET request: ~760ns
- Request with URL params: ~1050ns
- URL generation (1 param): ~226ns
- ID generation (unbound): ~167ns
- extractMethodInfo (cached): ~8ns

Memory per request:
- Simple GET: 1466B, 16 allocs
- With params: 2188B, 20 allocs
