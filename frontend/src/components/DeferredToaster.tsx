import { lazy, Suspense, useEffect, useState } from "react";
import { useTheme } from "@/lib/theme";

// Toasts are a reaction to an operator action, so the toaster does not need to
// be part of first paint. sonner is 33 kB raw (~9.5 kB gzip) and was the second
// largest vendor chunk on the login page, which never fires a toast.
//
// The chunk is fetched on the first idle window (2 s at the latest) instead of
// blocking the shell, and just for the authenticated layout: signed-out screens
// do not mount this component. sonner keeps its own store, so a toast fired
// before <Toaster> mounts is still rendered once it does.
const Toaster = lazy(() => import("sonner").then((m) => ({ default: m.Toaster })));

type IdleWindow = Window & {
  requestIdleCallback?: (cb: () => void, opts?: { timeout: number }) => number;
  cancelIdleCallback?: (id: number) => void;
};

export default function DeferredToaster() {
  const { resolved } = useTheme();
  const [ready, setReady] = useState(false);

  useEffect(() => {
    const w = window as IdleWindow;
    if (w.requestIdleCallback) {
      const id = w.requestIdleCallback(() => setReady(true), { timeout: 2000 });
      return () => w.cancelIdleCallback?.(id);
    }
    // Safari < 16.4 has no requestIdleCallback.
    const timer = setTimeout(() => setReady(true), 1500);
    return () => clearTimeout(timer);
  }, []);

  if (!ready) return null;
  return (
    <Suspense fallback={null}>
      <Toaster theme={resolved} richColors />
    </Suspense>
  );
}
