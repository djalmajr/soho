// Loader for the controller test: the panel imports Herdr's built-in modules,
// which only exist inside the app. The test resolves them to recording stubs so
// main.js itself can run under `node --test`.
const stubs = {
  "gpui-kit": "./gpui-kit.mjs",
  "gpui-base": "./gpui-base.mjs",
  herdr: "./herdr.mjs",
};

export function resolve(specifier, context, nextResolve) {
  const stub = stubs[specifier];
  if (stub) return { url: new URL(stub, import.meta.url).href, shortCircuit: true };
  return nextResolve(specifier, context);
}
