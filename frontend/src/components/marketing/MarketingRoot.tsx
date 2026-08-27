"use client";
// Marketing root wrapper — owns the light/dark theme (scoped to the marketing
// tree via [data-theme] on this element) and the toast host. The portal never
// mounts this and never sets data-theme, so it stays light-only. Theme resolves
// at mount time in a layout effect (before paint) to avoid a hydration flash:
// the server + first client render are always "light" (matching HTML), then the
// stored / system preference is applied before the browser paints.
import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from "react";

export type Theme = "light" | "dark";

const STORAGE_KEY = "pc-theme";

// useLayoutEffect on the server logs a warning; fall back to useEffect there.
const useIsoLayoutEffect = typeof window !== "undefined" ? useLayoutEffect : useEffect;

interface ThemeContextValue {
  theme: Theme;
  toggle: () => void;
}

const ThemeContext = createContext<ThemeContextValue | null>(null);

export function useMarketingTheme(): ThemeContextValue {
  const ctx = useContext(ThemeContext);
  if (!ctx) throw new Error("useMarketingTheme must be used within <MarketingRoot>");
  return ctx;
}

interface ToastContextValue {
  push: (text: string) => void;
}

const ToastContext = createContext<ToastContextValue | null>(null);

export function useMarketingToast(): ToastContextValue {
  const ctx = useContext(ToastContext);
  if (!ctx) throw new Error("useMarketingToast must be used within <MarketingRoot>");
  return ctx;
}

interface Toast {
  id: number;
  text: string;
}

function readInitialTheme(): Theme {
  try {
    const stored = window.localStorage.getItem(STORAGE_KEY);
    if (stored === "light" || stored === "dark") return stored;
  } catch {
    // localStorage can throw (private mode / disabled); fall through to system.
  }
  if (
    typeof window.matchMedia === "function" &&
    window.matchMedia("(prefers-color-scheme: dark)").matches
  ) {
    return "dark";
  }
  return "light";
}

export function MarketingRoot({ children }: { children: ReactNode }): ReactNode {
  const [theme, setTheme] = useState<Theme>("light");
  const [toasts, setToasts] = useState<Toast[]>([]);
  const timers = useRef<ReturnType<typeof setTimeout>[]>([]);

  // Resolve the real theme before first paint (no flash for dark-preferring users).
  useIsoLayoutEffect(() => {
    setTheme(readInitialTheme());
  }, []);

  useEffect(() => {
    const captured = timers.current;
    return () => captured.forEach(clearTimeout);
  }, []);

  const toggle = useCallback(() => {
    setTheme((prev) => {
      const next: Theme = prev === "dark" ? "light" : "dark";
      try {
        window.localStorage.setItem(STORAGE_KEY, next);
      } catch {
        // Persisting is best-effort; the toggle still works in-session.
      }
      return next;
    });
  }, []);

  const push = useCallback((text: string) => {
    const id = Date.now() + Math.random();
    setToasts((prev) => [...prev, { id, text }]);
    const timer = setTimeout(() => {
      setToasts((prev) => prev.filter((t) => t.id !== id));
    }, 3200);
    timers.current.push(timer);
  }, []);

  const themeValue = useMemo<ThemeContextValue>(() => ({ theme, toggle }), [theme, toggle]);
  const toastValue = useMemo<ToastContextValue>(() => ({ push }), [push]);

  return (
    <ThemeContext.Provider value={themeValue}>
      <ToastContext.Provider value={toastValue}>
        <div
          data-theme={theme}
          className="pc-marketing flex min-h-dvh flex-col bg-page text-ink"
          suppressHydrationWarning
        >
          {children}
          {/* Toast host lives inside the themed wrapper so toasts inherit the
              marketing dark tokens; fixed positioning keeps it out of flow. */}
          <div
            className="pointer-events-none fixed right-5 bottom-5 z-[60] flex flex-col gap-2"
            aria-live="polite"
            role="status"
          >
            {toasts.map((t) => (
              <div
                key={t.id}
                className="max-w-[320px] rounded-fluent border border-line border-l-[3px] border-l-accent bg-card px-4 py-3 text-[14px] text-ink shadow-pc-lift"
              >
                {t.text}
              </div>
            ))}
          </div>
        </div>
      </ToastContext.Provider>
    </ThemeContext.Provider>
  );
}
