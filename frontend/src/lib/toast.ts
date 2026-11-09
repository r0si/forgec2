import type * as Sonner from "sonner";

type ToastApi = typeof Sonner.toast;

let api: ToastApi | null = null;
let loading: Promise<ToastApi> | null = null;

function load(): Promise<ToastApi> {
  if (api) return Promise.resolve(api);
  if (!loading) {
    loading = import("sonner").then((m) => {
      api = m.toast;
      return api;
    });
  }
  return loading;
}

/**
 * Show a toast after the sonner chunk has loaded.
 *
 * sonner is 33 kB raw (~9.5 kB gzip). Every always-mounted shell component
 * (session warning, update banner/dialog, outbox toast, saved views) imported
 * it statically, which put the chunk in first paint even on the login page and
 * inside the entry's critical path. Toasts only happen after an operator
 * action, so the first call loads the chunk and later calls reuse it; the
 * deferred <DeferredToaster> mounts on idle in the authenticated shell.
 *
 * A failed chunk load is swallowed: the action that triggered the toast must
 * still complete.
 */
export function toastSoon(run: (toast: ToastApi) => void): void {
  void load()
    .then(run)
    .catch(() => {
      loading = null;
    });
}
