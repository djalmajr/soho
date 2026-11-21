// Event contexts expire when their callback returns. Spawned contexts remain
// usable until their own task settles, independently of their parent's lifetime.
export const STALE_MESSAGE = "cx is no longer valid: it was captured during an earlier call and used later. Use cx.spawn or take cx from the callback arguments instead.";

export function fakeCx(state = {
  staleUses: 0, notifies: 0, liveTasks: 0, tasks: [], timers: { every: [], after: [] },
}) {
  let active = true;
  const check = () => {
    if (active) return;
    state.staleUses += 1;
    throw new Error(STALE_MESSAGE);
  };
  const expire = () => { active = false; };
  const event = (callback) => {
    const cx = fakeCx(state);
    try { return callback(cx); } finally { cx.expire(); }
  };
  const timer = (kind, ms, callback) => {
    check();
    const entry = { ms, callback: () => event(callback), cancelled: false };
    state.timers[kind].push(entry);
    return { cancel() { entry.cancelled = true; } };
  };
  return {
    state,
    tasks: state.tasks,
    timers: state.timers,
    get notifies() { return state.notifies; },
    notify() { check(); state.notifies += 1; },
    focus_handle() { check(); return { focus() {} }; },
    timer: {
      every(ms, callback) { return timer("every", ms, callback); },
      after(ms, callback) { return timer("after", ms, callback); },
    },
    spawn(body) {
      check();
      const cx = fakeCx(state);
      state.liveTasks += 1;
      const promise = (async () => {
        try {
          return await body(cx);
        } finally {
          cx.expire();
          state.liveTasks -= 1;
        }
      })();
      state.tasks.push(promise);
      return {};
    },
    expire,
    event,
  };
}

export const tick = () => new Promise((resolve) => setImmediate(resolve));

export async function flush(cx) {
  // Held reads and long-lived tasks cannot block the microtask drain.
  for (let i = 0; i < 80; i += 1) {
    const pending = cx.tasks.splice(0);
    for (const task of pending) task.catch(() => {});
    if (pending.length) await Promise.race([Promise.allSettled(pending), tick()]);
    await tick();
    if (!pending.length && cx.tasks.length === 0) return;
  }
}
