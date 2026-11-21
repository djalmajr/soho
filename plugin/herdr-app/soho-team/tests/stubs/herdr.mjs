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
  watches: [],
  watch: null,
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
  state.watches = [];
  state.watch = null;
  localStorage.clear?.();
}

// A pending folder watch the test can settle, cancel or reject. Each call
// returns one folder watch, as the app API does.
function makeWatch(folder) {
  let resolve;
  let failWatch;
  const promise = new Promise((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    failWatch = rejectPromise;
  });
  const handle = {
    folder,
    cancel() {
      if (entry.settled) return;
      entry.settled = true;
      resolve([]);
    },
    then(onFulfilled, onRejected) { return promise.then(onFulfilled, onRejected); },
  };
  const entry = {
    folder,
    handle,
    settled: false,
    emit(paths) {
      if (entry.settled) return false;
      entry.settled = true;
      resolve(paths);
      return true;
    },
    fail(error) {
      if (entry.settled) return false;
      entry.settled = true;
      failWatch(error);
      return true;
    },
  };
  state.watches.push(entry);
  return handle;
}

export function emit(folder, paths) {
  for (let i = state.watches.length - 1; i >= 0; i -= 1) {
    const entry = state.watches[i];
    if (entry.folder === folder && !entry.settled) return entry.emit(paths);
  }
  return false;
}

export function fail(folder, error) {
  for (let i = state.watches.length - 1; i >= 0; i -= 1) {
    const entry = state.watches[i];
    if (entry.folder === folder && !entry.settled) return entry.fail(error);
  }
  return false;
}

export function watchFolder(folder) {
  state.calls.push({ name: "watchFolder", args: [folder] });
  if (typeof state.watch === "function") return state.watch(folder);
  return makeWatch(folder);
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

export function activeWatches() {
  return state.watches.filter((entry) => !entry.settled).map((entry) => entry.folder);
}

// The command loop must not spin: it ends after one tick with a rejection that
// no caller observes.
export function nextCommand() {
  return new Promise((_resolve, reject) => {
    setImmediate(() => reject(new Error("no command in this test")));
  });
}
