"use client";
// A coarse ticking clock so relative/countdown labels stay live without pulling
// a new render on every animation frame. Components read `useNow()` and derive
// their own display string (formatCountdown, nextRun) from it.
import { useEffect, useState } from "react";

/** Re-renders every `ms` (default 30s) and returns the current Date. */
export function useNow(ms = 30_000): Date {
  const [now, setNow] = useState(() => new Date());
  useEffect(() => {
    const id = setInterval(() => setNow(new Date()), ms);
    return () => clearInterval(id);
  }, [ms]);
  return now;
}
