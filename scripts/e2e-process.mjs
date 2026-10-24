import { spawn } from 'node:child_process';
import { rmSync } from 'node:fs';
import { constants } from 'node:os';
import { setTimeout as delay } from 'node:timers/promises';

const signalCode = signal => constants.signals[signal] ? 128 + constants.signals[signal] : 1;

// The detached Unix process group belongs only to this fixture. Never inspect
// process names or signal a shared runner group when cleaning up descendants.
function signalChild(child, signal) {
  if (!child?.pid) return false;
  try {
    if (process.platform === 'win32') return child.kill(signal);
    process.kill(-child.pid, signal);
    return true;
  } catch (error) {
    if (error.code === 'ESRCH') return false;
    throw error;
  }
}

async function stopChild(child, exited, graceMs, killMs) {
  if (!child?.pid) return;
  let permissionError;
  const signal = value => {
    try {
      const alive = signalChild(child, value);
      if (!alive) permissionError = undefined;
      return alive;
    } catch (error) {
      if (error.code !== 'EPERM') throw error;
      // Darwin can return EPERM for a group containing only unreaped zombies.
      // This is not proof of exit: retry within the deadline, and keep a real
      // permission failure visible if the group never becomes absent.
      permissionError = error;
      return true;
    }
  };
  const pending = () => {
    const groupAlive = process.platform !== 'win32' && signal(0);
    // A group disappearing does not itself reap the ChildProcess. Do not unref
    // its handle or remove the directory until its exit event has also arrived.
    return groupAlive || !exited();
  };
  if (!pending()) return;
  signal('SIGINT');
  let deadline = performance.now() + graceMs;
  while (pending() && performance.now() < deadline) await delay(20);
  if (pending()) signal('SIGKILL');
  deadline = performance.now() + killMs;
  while (pending() && performance.now() < deadline) await delay(20);
  if (pending()) throw permissionError ?? new Error('fixture child did not exit after SIGKILL');
}

async function closeSink(sink) {
  // Active/keep-alive requests must not hold the fixture open after its daemon.
  await new Promise(resolve => {
    sink.close(() => resolve());
    sink.closeAllConnections();
  });
}

/** Own one fixture's process, sink and directory, including failed preparation. */
export async function runFixtureProcess({ command, args = [], root, sink, prepare = async () => {},
  graceMs = 5000, killMs = 1000, parentCheckMs = 250 }) {
  const controller = new AbortController();
  const parent = process.ppid;
  let requestedSignal;
  let child;
  let exited = false;
  let code = 1;
  const stop = signal => {
    requestedSignal ??= signal;
    controller.abort();
  };
  const handlers = new Map(['SIGINT', 'SIGTERM'].map(signal => [signal, () => stop(signal)]));
  for (const [signal, handler] of handlers) process.on(signal, handler);
  // Playwright normally signals us. Also stop if its runner disappears without
  // sending a signal; an orphaned fixture must not keep its daemon alive.
  const parentCheck = setInterval(() => {
    if (process.ppid !== parent) { stop('SIGTERM'); return; }
    try { process.kill(parent, 0); }
    catch (error) { if (error.code === 'ESRCH') stop('SIGTERM'); }
  }, parentCheckMs);
  parentCheck.unref();
  try {
    await prepare(controller.signal);
    if (!controller.signal.aborted) {
      child = spawn(command, args, { stdio: 'inherit', detached: process.platform !== 'win32' });
      const finished = new Promise(resolve => {
        child.once('error', error => { exited = true; resolve({ error }); });
        child.once('exit', (code, signal) => { exited = true; resolve({ code: code ?? signalCode(signal) }); });
      });
      const stopped = new Promise(resolve => {
        if (controller.signal.aborted) resolve({ stopped: true });
        else controller.signal.addEventListener('abort', () => resolve({ stopped: true }), { once: true });
      });
      const result = await Promise.race([finished, stopped]);
      if (result.error) throw result.error;
      code = result.stopped ? signalCode(requestedSignal) : result.code;
    } else code = signalCode(requestedSignal);
  } catch (error) {
    if (controller.signal.aborted && error.name === 'AbortError') code = signalCode(requestedSignal);
    else console.error(`Browser fixture failed (${error.code ?? error.name ?? 'Error'}).`);
  } finally {
    clearInterval(parentCheck);
    try {
      await stopChild(child, () => exited, graceMs, killMs);
    } catch (error) {
      code = 1;
      console.error(`Browser fixture cleanup failed (${error.code ?? error.name ?? 'Error'}).`);
    } finally {
      child?.unref();
      try { await closeSink(sink); }
      finally {
        rmSync(root, { recursive: true, force: true });
        for (const [signal, handler] of handlers) process.off(signal, handler);
      }
    }
  }
  return code;
}
