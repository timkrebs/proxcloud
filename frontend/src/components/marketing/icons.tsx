// Marketing SVG icon set — extracted verbatim from the design source's svc()
// and line() helpers (proxcloud-website-design.html). These product marks
// (db / store / catalog / secrets / dns) are NOT in the portal's icon set, so
// they live here; the shared vm / k8s / net marks are reproduced too so the
// marketing tree is self-contained. All icons are decorative → aria-hidden.
import type { ReactElement } from "react";

type Shape = [tag: "rect" | "path" | "circle" | "ellipse", attrs: Record<string, string | number>];

const SVC_SHAPES: Record<string, Shape[]> = {
  vm: [
    ["rect", { x: 1, y: 2.5, width: 18, height: 12.5, rx: 0.8, fill: "#005BA1" }],
    ["rect", { x: 2.6, y: 4.1, width: 14.8, height: 9.3, fill: "#50E6FF" }],
    ["path", { d: "M2.6 13.4 11.5 4.1h5.9v9.3z", fill: "#0078D4" }],
    ["rect", { x: 6.5, y: 16.2, width: 7, height: 1.6, rx: 0.5, fill: "#8A8886" }],
  ],
  k8s: [
    ["path", { d: "M10 1.2 17.8 5.7v9L10 19.2 2.2 14.7v-9z", fill: "#0078D4" }],
    ["circle", { cx: 10, cy: 10, r: 2.2, fill: "#fff" }],
    ["circle", { cx: 10, cy: 4.8, r: 1.4, fill: "#50E6FF" }],
    ["circle", { cx: 5.2, cy: 12.8, r: 1.4, fill: "#50E6FF" }],
    ["circle", { cx: 14.8, cy: 12.8, r: 1.4, fill: "#50E6FF" }],
    ["path", { d: "M10 6.2v1.8M6.4 12.1l1.8-1M13.6 12.1l-1.8-1", stroke: "#C3F1FF", strokeWidth: 1.1 }],
  ],
  db: [
    ["path", { d: "M3 4.6v10.8c0 1.55 3.1 2.8 7 2.8s7-1.25 7-2.8V4.6", fill: "#0078D4" }],
    ["ellipse", { cx: 10, cy: 4.6, rx: 7, ry: 2.7, fill: "#50E6FF" }],
    ["ellipse", { cx: 10, cy: 10.2, rx: 7, ry: 2.4, fill: "#005BA1", opacity: 0.55 }],
  ],
  net: [
    ["path", { d: "M10 4.5 4 15M10 4.5 16 15M4 15h12", stroke: "#50E6FF", strokeWidth: 1.6 }],
    ["circle", { cx: 10, cy: 4, r: 2.6, fill: "#0078D4" }],
    ["circle", { cx: 4, cy: 15.5, r: 2.6, fill: "#0078D4" }],
    ["circle", { cx: 16, cy: 15.5, r: 2.6, fill: "#005BA1" }],
  ],
  store: [
    ["rect", { x: 2.5, y: 3, width: 15, height: 4.2, rx: 1, fill: "#005BA1" }],
    ["rect", { x: 2.5, y: 8.2, width: 15, height: 4.2, rx: 1, fill: "#0078D4" }],
    ["rect", { x: 2.5, y: 13.4, width: 15, height: 4.2, rx: 1, fill: "#50E6FF" }],
  ],
  catalog: [
    ["rect", { x: 2, y: 2, width: 7.2, height: 7.2, rx: 1, fill: "#0078D4" }],
    ["rect", { x: 10.8, y: 2, width: 7.2, height: 7.2, rx: 1, fill: "#50E6FF" }],
    ["rect", { x: 2, y: 10.8, width: 7.2, height: 7.2, rx: 1, fill: "#50E6FF" }],
    ["rect", { x: 10.8, y: 10.8, width: 7.2, height: 7.2, rx: 1, fill: "#005BA1" }],
  ],
  secrets: [
    ["rect", { x: 3.5, y: 8.5, width: 13, height: 9.5, rx: 1.2, fill: "#0078D4" }],
    ["path", { d: "M6.6 8.5V6.2a3.4 3.4 0 0 1 6.8 0v2.3", stroke: "#005BA1", strokeWidth: 1.8, fill: "none" }],
    ["circle", { cx: 10, cy: 13, r: 1.7, fill: "#C3F1FF" }],
  ],
  dns: [
    ["circle", { cx: 10, cy: 10, r: 8, fill: "#0078D4" }],
    ["path", { d: "M2 10h16M10 2c-4.2 4.4-4.2 11.6 0 16 4.2-4.4 4.2-11.6 0-16", stroke: "#C3F1FF", strokeWidth: 1.2, fill: "none" }],
  ],
};

export type MarketingSvcName = keyof typeof SVC_SHAPES;

/** Multicolor product mark — 20×20 viewBox, decorative. */
export function MarketingSvc({
  name,
  size = 30,
}: {
  name: MarketingSvcName;
  size?: number;
}): ReactElement {
  const shapes = SVC_SHAPES[name] ?? SVC_SHAPES.catalog;
  return (
    <svg width={size} height={size} viewBox="0 0 20 20" className="shrink-0" aria-hidden>
      {shapes.map(([tag, attrs], i) => {
        const Tag = tag as "rect";
        return <Tag key={i} fill="none" {...attrs} />;
      })}
    </svg>
  );
}

/** Single-path line icon — 16×16 viewBox, stroked, round caps (design line()). */
export function LineIcon({
  d,
  size = 20,
  strokeWidth = 1.4,
  className = "text-accent",
}: {
  d: string;
  size?: number;
  strokeWidth?: number;
  className?: string;
}): ReactElement {
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 16 16"
      fill="none"
      stroke="currentColor"
      strokeWidth={strokeWidth}
      strokeLinecap="round"
      strokeLinejoin="round"
      className={`shrink-0 ${className}`}
      aria-hidden
    >
      <path d={d} />
    </svg>
  );
}

/** Green check used in bullet lists (design check path). */
export function CheckIcon({ size = 15 }: { size?: number }): ReactElement {
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 16 16"
      fill="none"
      stroke="#107C10"
      strokeWidth={1.6}
      strokeLinecap="round"
      strokeLinejoin="round"
      className="mt-1 shrink-0"
      aria-hidden
    >
      <path d="M3 8.5l3.2 3L13 4.5" />
    </svg>
  );
}

/** Small forward chevron used on CTAs (design arrow path). */
export function ArrowIcon({ size = 13 }: { size?: number }): ReactElement {
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 16 16"
      fill="none"
      stroke="currentColor"
      strokeWidth={1.6}
      strokeLinecap="round"
      strokeLinejoin="round"
      className="shrink-0"
      aria-hidden
    >
      <path d="M6 3.5 10.5 8 6 12.5" />
    </svg>
  );
}

/** Brand hexagon logo (design header/footer mark). */
export function MarketingLogo({ size = 26 }: { size?: number }): ReactElement {
  return (
    <svg width={size} height={size} viewBox="0 0 20 20" aria-hidden>
      <path d="M10 1.5 18 6v8l-8 4.5L2 14V6z" fill="#0078D4" />
      <path d="M10 1.5 18 6l-8 4.5L2 6z" fill="#50E6FF" />
      <path d="M10 10.5V19L2 14V6z" fill="#005BA1" />
    </svg>
  );
}
