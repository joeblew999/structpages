# workerd's config for a TinyGo build of a structpages app, written next to it by structpages-workers:
# workerd serve config.capnp serves it on :8080.
using Workerd = import "/workerd/workerd.capnp";

const config :Workerd.Config = (
  services = [(name = "app", worker = .app)],
  sockets = [(name = "http", address = "*:8080", http = (), service = "app")],
);

const app :Workerd.Worker = (
  modules = [
    (name = "worker.mjs", esModule = embed "worker.mjs"),
    (name = "runtime.mjs", esModule = embed "runtime.mjs"),
    (name = "wasm_exec.js", esModule = embed "wasm_exec.js"),
    (name = "app.wasm", wasm = embed "app.wasm"),
  ],
  compatibilityDate = "2026-10-01",
);
