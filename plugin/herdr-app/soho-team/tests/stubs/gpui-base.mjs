// Recording stand-in for "gpui-base" (layout helpers and the text input), for
// the controller test only.
import { element } from "./gpui-kit.mjs";

export const h_flex = () => element();
export const v_flex = () => element();

export const Popover = { new: () => element() };
export const TextView = { markdown: () => element() };

export class InputState {
  constructor(options = {}) {
    this.current = options.value ?? "";
    this.handlers = {};
    this.released = false;
  }

  static new(options) {
    return new InputState(options);
  }

  value() {
    return this.current;
  }

  set_value(next) {
    this.current = next;
  }

  replace(next) {
    this.current = next;
  }

  on(event, handler) {
    this.handlers[event] = handler;
    return true;
  }

  // Test helper: type a value the way the widget would report it.
  type(next, cx) {
    this.current = next;
    this.handlers.change?.({}, cx);
    return true;
  }

  focus() {}
  release() { this.released = true; return true; }
}

export const Input = { new: () => element() };
