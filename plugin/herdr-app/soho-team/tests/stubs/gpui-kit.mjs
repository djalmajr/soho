// Recording stand-in for "gpui-kit" (views and elements), for the controller
// test only. Nothing here draws: every builder method returns the element and
// records the click or size handler it was given.
export function element(record = { children: [], ids: [], handlers: {}, calls: [] }) {
  const proxy = new Proxy(function noop() {}, {
    get(_target, prop) {
      if (prop === "then") return undefined;
      if (prop === "record") return record;
      if (prop === "child") return (child) => { record.children.push(child); return proxy; };
      if (prop === "children") {
        return (items) => { for (const item of items) record.children.push(item); return proxy; };
      }
      if (prop === "when") return (condition, branch) => (condition ? (branch(proxy) ?? proxy) : proxy);
      if (prop === "map") return (transform) => transform(proxy);
      if (prop === "id") return (name) => { record.ids.push(name); return proxy; };
      if (typeof prop === "string" && prop.startsWith("on_")) {
        return (handler) => { record.handlers[prop] = handler; return proxy; };
      }
      if (prop === "hover") return (fn) => { record.handlers.hover = fn; return proxy; };
      // Every other builder call is a style: keep it so a test can compare two
      // states of the same component.
      return (...args) => { record.calls.push([prop, ...args]); return proxy; };
    },
  });
  return proxy;
}

export class View {}

export const div = () => element();
export const svg = () => element();
