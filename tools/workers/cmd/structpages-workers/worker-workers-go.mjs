// The Worker as structpages-workers -glue workers-go writes it, to measure what worker.mjs saves: one Go
// runtime per isolate, started on its first request and given every request after (workers.Serve never
// returns), each request going through workers-go's own request path, value by value. workers-go reads env
// and ctx from the context a runtime starts with, so here they are the first request's.
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
