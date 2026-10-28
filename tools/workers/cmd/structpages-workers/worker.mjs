// The Worker a TinyGo build of a structpages app runs as. structpages-workers writes it next to the
// wasm_exec.js and runtime.mjs that workers-go's workers-assets-gen writes, over that tool's own
// worker.mjs, which starts a new Go instance for every request: main and Mount every time, and
// nothing kept between requests. This one starts one per isolate, on its first request, and gives it
// every request after: workers.Serve never returns, so its runtime stays alive.
//
// workers-go reads env and ctx from the context a runtime starts with, so here they are the first
// request's. The examples use no Cloudflare bindings; an app that does needs them per request.
import "./wasm_exec.js";
import { createRuntimeContext, loadModule } from "./runtime.mjs";

globalThis.tryCatch = (fn) => {
  try {
    return { result: fn() };
  } catch (e) {
    return { error: e };
  }
};

let running; // resolves to the binding of the isolate's Go runtime, once it is ready

function start(env, ctx) {
  running ??= (async () => {
    const go = new Go();
    const binding = {};
    let ready;
    const isReady = new Promise((resolve) => (ready = resolve));
    const instance = new WebAssembly.Instance(await loadModule(), {
      ...go.importObject,
      workers: { ready: () => ready() },
    });
    go.run(instance, createRuntimeContext({ env, ctx, binding }));
    await isReady;
    return binding;
  })().catch((error) => {
    running = undefined; // let the next request try again
    throw error;
  });
  return running;
}

export default {
  async fetch(req, env, ctx) {
    const binding = await start(env, ctx);
    return binding.handleRequest(req);
  },
};
