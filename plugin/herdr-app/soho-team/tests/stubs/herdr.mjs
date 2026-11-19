// Stand-in for "herdr" (the app bridge), for the controller test only. The test
// sets `state` before driving the panel; `exec` records every call and answers
// through `state.exec`.
export const state = {
  spaces: [],
  focused: null,
  checking: false,
  exec: async () => ({ code: 0, stdout: "", stderr: "" }),
  calls: [],
  overlays: [],
  notices: [],
  written: [],
};

export function reset() {
  state.spaces = [];
  state.focused = null;
  state.checking = false;
  state.exec = async () => ({ code: 0, stdout: "", stderr: "" });
  state.readText = null;
  state.calls = [];
  state.overlays = [];
  state.notices = [];
  localStorage.clear?.();
}

const hue = new Proxy({}, {
  get(_target, prop) {
    if (prop === "then") return undefined;
    return "#000000";
  },
});

export function theme() {
  return hue;
}

export function spaces() {
  return state.spaces;
}

export function focused() {
  return state.focused;
}

export function checking() {
  return state.checking;
}

export async function exec(name, args, options) {
  state.calls.push({ name, args, options });
  return state.exec(name, args, options);
}

export function run(name, args) {
  state.calls.push({ name, args, run: true });
  return null;
}

export function openOverlay(spec) {
  state.overlays.push(spec);
  return `overlay-${state.overlays.length}`;
}

export async function readTextFile(path, options) {
  state.calls.push({ name: "readTextFile", args: [path], options });
  if (typeof state.readText === "function") return state.readText(path, options);
  return null;
}

// The command loop must not spin: it ends after one tick with a rejection that
// no caller observes.
export function nextCommand() {
  return new Promise((_resolve, reject) => {
    setImmediate(() => reject(new Error("no command in this test")));
  });
}
