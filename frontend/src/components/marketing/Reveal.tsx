"use client";
// Scroll-reveal wrapper. Progressive enhancement: the server renders children
// visible (no `pc-reveal` class), so no-JS visitors and crawlers always see
// content. On mount — before paint, and only when motion is allowed — it arms
// the reveal and an IntersectionObserver adds `is-in` as the element scrolls
// into view. prefers-reduced-motion skips the effect entirely (stays visible).
import { useEffect, useLayoutEffect, useRef, useState, type ReactNode } from "react";

const useIsoLayoutEffect = typeof window !== "undefined" ? useLayoutEffect : useEffect;

export function Reveal({ children, className = "" }: { children: ReactNode; className?: string }) {
  const ref = useRef<HTMLDivElement>(null);
  const [armed, setArmed] = useState(false);

  useIsoLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    if (
      typeof window.matchMedia === "function" &&
      window.matchMedia("(prefers-reduced-motion: reduce)").matches
    ) {
      return;
    }
    if (typeof IntersectionObserver === "undefined") return;
    setArmed(true);
    const io = new IntersectionObserver(
      (entries) => {
        entries.forEach((entry) => {
          if (entry.isIntersecting) {
            entry.target.classList.add("is-in");
            io.unobserve(entry.target);
          }
        });
      },
      { rootMargin: "0px 0px -8% 0px", threshold: 0.08 },
    );
    io.observe(el);
    return () => io.disconnect();
  }, []);

  return (
    <div ref={ref} className={`${armed ? "pc-reveal" : ""} ${className}`.trim()}>
      {children}
    </div>
  );
}
