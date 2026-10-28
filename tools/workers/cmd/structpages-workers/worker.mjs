// The Worker a TinyGo build of a structpages app runs as. structpages-workers writes it next to the
// wasm_exec.js and runtime.mjs that workers-go's workers-assets-gen writes, over that tool's own worker.mjs,
// which starts a Go runtime for every request: main and Mount every time, and nothing kept between them.
// workers.Serve, in structpages' workers module, is the Go side of what this file does:
//
//   - Go runtimes are kept between requests. A runtime serves one request at a time; one that finds none
//     waiting starts its own, so nothing waits for another request. A runtime is kept for the next request
//     only when its answer ended normally and Go says its heap has room for another (full, with its
//     answer, false before the collector would have to run); at most maxWaiting are kept.
//   - maxWarm runtimes start while the module loads, each answering a GET of warmPaths into nothing, so a
//     new isolate's first request finds one ready, its code already run once.
//   - A request goes to Go as numbers only: its method, URL and headers, as lines, and its body are written
//     into the runtime's memory, and Go's structpages_serve is called with their lengths; then TinyGo's
//     scheduler is run, which answers it. The answer comes back the same way, in one call (respond) that
//     reads it out of that memory. No JavaScript value is made for either, where workers-go's own request
//     path crosses between JavaScript and Go some hundred times, and a js.FuncOf call a dozen.
//   - TinyGo's sleeps are rounded up to a whole millisecond: on Cloudflare the clock moves only by whole
//     milliseconds, so a shorter sleep never ends (tinygo-org/tinygo#5798).
//   - While the module loads Cloudflare gives no crypto randomness, so TinyGo's runtime, which takes its
//     seed there, gets Math.random's; anything else asking for random bytes then fails the warm start.
//
// A request with the header x-go-runtime gets the kind of runtime that answered it in its own: new, warm or
// reused (and why there was no warm one, if start-up failed while the module loaded).
import "./wasm_exec.js"; // TinyGo's, for its global Go class
import { createRuntimeContext, loadModule } from "./runtime.mjs";

const warmPaths = ["/"]; // structpages-workers -warm
const maxWarm = 2; // runtimes started while the module loads
const maxWaiting = 4; // runtimes kept for the next request; each holds its Go heap

// Every sleep TinyGo asks for, rounded up to a whole millisecond. While serve gives Go a request, TinyGo's
// call for its scheduler to run as soon as it can is not needed: serve runs it straight after.
const TinyGo = globalThis.Go;
globalThis.Go = class extends TinyGo {
  constructor() {
    super();
    const sleepTicks = this.importObject.gojs["runtime.sleepTicks"];
    this.importObject.gojs["runtime.sleepTicks"] = (ns) => {
      if (this.entering && Number(ns) === 0) return;
      sleepTicks(Math.ceil(Number(ns) / 1e6) * 1e6);
    };
  }
};

const encoder = new TextEncoder();
const decoder = new TextDecoder();

// workers-go's Go code calls this.
globalThis.tryCatch = (fn) => {
  try {
    return { result: fn() };
  } catch (error) {
    return { error };
  }
};

// seedOnly says whether a call for random bytes comes from TinyGo's runtime taking its seed as the program
// starts: the only Wasm functions on the stack are arc4random, runtime.hardwareRand (when not inlined) and
// _start. Package initialisers and main run further down, in a goroutine.
function seedOnly(stack) {
  const frames = stack.split("\n").filter((line) => line.includes("wasm://"));
  return (
    frames.length > 0 &&
    frames.every((line) => /\.(arc4random|runtime\.hardwareRand|_start(\.command_export)?) \(/.test(line))
  );
}

const waiting = []; // runtimes ready for a request
let warmFailure = ""; // why no runtime started while the module loaded, for x-go-runtime

// begin starts a runtime. Go's start-up runs inside go.run up to workers.Ready(): started says whether it
// got there before go.run returned; isReady resolves when it does.
function begin(module, env, ctx, warm) {
  const go = new Go();
  const binding = warm ? { warm } : {};
  const context = createRuntimeContext({ env, ctx, binding });
  // pointer and capacity: where Go's request buffer is, and its size (structpages_buffer).
  const runtime = { go, binding, context, started: false, served: 0, warm: Boolean(warm), pointer: 0, capacity: 0 };
  runtime.isReady = new Promise((resolve) => {
    const wasi = go.importObject.wasi_snapshot_preview1;
    const random = wasi.random_get;
    wasi.random_get = (pointer, length) => {
      if (runtime.started || !warm) return random(pointer, length);
      if (!seedOnly(new Error().stack)) throw new Error("random bytes wanted during start-up, other than the runtime's seed");
      new Uint8Array(go._inst.exports.memory.buffer, pointer >>> 0, length >>> 0).forEach(
        (_, i, bytes) => (bytes[i] = Math.random() * 256),
      );
      return 0;
    };
    const instance = new WebAssembly.Instance(module, {
      ...go.importObject,
      workers: {
        ready: () => {
          runtime.started = true;
          resolve();
        },
      },
      structpages: answers(runtime),
    });
    runtime.exports = instance.exports;
    go.run(instance, context).catch((error) => {
      if (warm) warmFailure = `start-up failed while the module loaded: ${String(error?.stack || error).replace(/\s+/g, " ").slice(0, 900)}`;
    });
  });
  return runtime;
}

async function start(env, ctx) {
  const runtime = begin(await loadModule(), env, ctx);
  await runtime.isReady;
  return runtime;
}

// done keeps a runtime that answered normally for the next request, unless Go said its heap is full.
function done(runtime) {
  if (!runtime.go.exited && !runtime.full && waiting.length < maxWaiting) waiting.push(runtime);
}

// warm starts maxWarm runtimes while the module loads. If Go's start-up can't finish there, requests start
// runtimes as they would without it.
async function warm() {
  try {
    const module = await loadModule();
    for (let i = 0; i < maxWarm && waiting.length < maxWaiting; i++) {
      const runtime = begin(module, {}, undefined, warmPaths);
      if (!runtime.started) {
        warmFailure ||= "start-up did not reach workers.Ready() while the module loaded";
        return;
      }
      waiting.push(runtime);
    }
  } catch (error) {
    warmFailure = `start-up failed while the module loaded: ${String(error)}`;
  }
}

// answers are the two functions Go answers a runtime's request through, workers.Serve's respond and write,
// the request's own state in runtime.request. Each copies its bytes out of Go's memory during the call, as
// Go reuses them after it.
function answers(runtime) {
  const copy = (at, length) => (length ? runtime.exports.memory.buffer.slice(at, at + length) : null);
  return {
    respond(status, headAt, headLength, bodyAt, bodyLength, more, full) {
      const request = runtime.request;
      runtime.full = full === 1;
      const body = copy(bodyAt, bodyLength);
      const headers = new Headers();
      const lines = decoder.decode(new Uint8Array(runtime.exports.memory.buffer, headAt, headLength)).split("\n");
      for (let i = 0; i + 1 < lines.length; i += 2) headers.append(lines[i], lines[i + 1]);
      if (request.label) headers.set("x-go-runtime", request.label);
      // The whole answer: the runtime is free for the next request.
      if (!more) {
        done(runtime);
        return request.resolve(new Response(body, { status, headers }));
      }
      // A stream: the runtime is free when Go has written the last of it. If the client goes away first,
      // Go is told (its request's context is cancelled) and the runtime is dropped.
      const readable = new ReadableStream({
        start(controller) {
          request.stream = controller;
          if (body) controller.enqueue(new Uint8Array(body));
        },
        cancel() {
          request.stream = null;
          runtime.exports.structpages_cancel();
        },
      });
      request.resolve(new Response(readable, { status, headers }));
    },
    write(bodyAt, bodyLength, more, full) {
      const request = runtime.request;
      if (!request.stream) return 0;
      runtime.full = full === 1;
      const body = copy(bodyAt, bodyLength);
      if (body) request.stream.enqueue(new Uint8Array(body));
      if (!more) {
        request.stream.close();
        request.stream = null;
        done(runtime);
      }
      return 1;
    },
  };
}

// serve gives a request to a Go runtime, written into its memory, and takes its answer in one call when the
// handler did not stream.
async function serve(request, env, ctx) {
  let lines = `${request.method}\n${request.url}`;
  request.headers.forEach((value, name) => (lines += `\n${name}\n${value}`));
  const body = request.body ? new Uint8Array(await request.arrayBuffer()) : null;
  let runtime = waiting.pop();
  const kind = runtime ? (runtime.served ? "reused" : runtime.warm ? "warm" : "new") : "new";
  if (runtime) {
    // The bindings and waitUntil are this request's.
    runtime.context.env = env;
    runtime.context.ctx = ctx;
  } else {
    runtime = await start(env, ctx);
  }
  runtime.served++;
  const label = request.headers.has("x-go-runtime") ? (warmFailure ? `${kind}; ${warmFailure}` : kind) : "";
  return new Promise((resolve, reject) => {
    runtime.request = { resolve, stream: null, label };
    try {
      const { exports } = runtime;
      // Room for the head as UTF-8, at most three bytes for each of its UTF-16 units, and the body.
      const bodyLength = body ? body.length : 0;
      const size = lines.length * 3 + bodyLength;
      if (size > runtime.capacity) {
        runtime.pointer = exports.structpages_buffer(size) >>> 0;
        runtime.capacity = size;
      }
      const memory = new Uint8Array(exports.memory.buffer, runtime.pointer, runtime.capacity);
      const headLength = encoder.encodeInto(lines, memory).written;
      if (body) memory.set(body, headLength);
      runtime.go.entering = true;
      try {
        exports.structpages_serve(headLength, bodyLength);
      } finally {
        runtime.go.entering = false;
      }
      exports.go_scheduler();
    } catch (error) {
      reject(error); // the runtime is not kept
    }
  });
}

await warm();

export default { fetch: serve };
