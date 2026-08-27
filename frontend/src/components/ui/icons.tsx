// Icon library — SVG path data extracted verbatim from the design source
// (Proxcloud.dc.html). Three systems: line icons (Mi), filled micro-icons
// (Fi), and multicolor product icons (Svc), plus the brand logo, spinner,
// and top-bar one-offs.
import type { CSSProperties } from "react";

const MI_PATHS = {
  home: "M2.5 7.5 8 2.5l5.5 5M4 7v6.5h8V7",
  person: "M8 7.5a2.8 2.8 0 1 0 0-5.6 2.8 2.8 0 0 0 0 5.6zM2.5 14c.8-2.6 2.9-4 5.5-4s4.7 1.4 5.5 4",
  clock: "M8 4.5V8l2.5 1.5M14.5 8a6.5 6.5 0 1 1-13 0 6.5 6.5 0 0 1 13 0",
  gear: "M8 10.5A2.5 2.5 0 1 0 8 5.5a2.5 2.5 0 0 0 0 5zM8 1.2v2M8 12.8v2M1.2 8h2M12.8 8h2M3.2 3.2l1.4 1.4M11.4 11.4l1.4 1.4M12.8 3.2l-1.4 1.4M4.6 11.4l-1.4 1.4",
  grid: "M2 2h5v5H2zM9 2h5v5H9zM2 9h5v5H2zM9 9h5v5H9z",
  tag: "M2 2h5.5L14 8.5 8.5 14 2 7.5zM5 4.8h.01",
  globe:
    "M14.5 8a6.5 6.5 0 1 1-13 0 6.5 6.5 0 0 1 13 0M1.5 8h13M8 1.5c-4.2 3.8-4.2 9.2 0 13 4.2-3.8 4.2-9.2 0-13",
  disk: "M14.5 4.5c0 1.4-2.9 2.5-6.5 2.5S1.5 5.9 1.5 4.5 4.4 2 8 2s6.5 1.1 6.5 2.5zM1.5 4.5v7C1.5 12.9 4.4 14 8 14s6.5-1.1 6.5-2.5v-7",
  camera: "M2 5h3l1.5-2h3L11 5h3v8H2zM8 11a2.5 2.5 0 1 0 0-5 2.5 2.5 0 0 0 0 5z",
  resize: "M2 9v5h5M14 7V2H9M2 14 7 9M14 2 9 7",
  chart: "M2 13.5h12M3.5 11l3-4 2.5 2 3.5-5",
  restart: "M13.5 8A5.5 5.5 0 1 1 8 2.5c1.8 0 3.4.85 4.4 2.2M12.8 1.6v3.4H9.4",
  trash: "M2.5 4h11M6 4V2.5h4V4M4.5 4l.6 9.5h5.8L11.5 4M6.7 6.5v4.6M9.3 6.5v4.6",
  console: "M1.5 3.5h13v9h-13zM4 6.5 6.5 8.5 4 10.5M8 10.5h4",
  dots: "M3.2 8h.01M8 8h.01M12.8 8h.01",
  search: "M7 12A5 5 0 1 0 7 2a5 5 0 0 0 0 10zM10.7 10.7 14 14",
  check: "M3 8.5l3.2 3L13 4.5",
  checkC: "M8 14.5A6.5 6.5 0 1 0 8 1.5a6.5 6.5 0 0 0 0 13zM5 8.2l2.2 2.2L11.2 6",
  info: "M8 14.5A6.5 6.5 0 1 0 8 1.5a6.5 6.5 0 0 0 0 13zM8 7.5v4M8 4.6h.01",
  warn: "M8 2 15 13.5H1zM8 6.5v3.2M8 12h.01",
  plus: "M8 2.5v11M2.5 8h11",
  bolt: "M9 1.5 3 9h4l-1 5.5L12 7H8z",
  bell: "M12.5 6.5a4.5 4.5 0 0 0-9 0V10l-1.5 2h12L12.5 10zM6.3 14a1.8 1.8 0 0 0 3.4 0",
  help: "M8 14.5A6.5 6.5 0 1 0 8 1.5a6.5 6.5 0 0 0 0 13zM6.2 6.2A1.8 1.8 0 1 1 8 8.2v1M8 11.6h.01",
  copy: "M4.5 5.5h7v8h-7zM6.5 5.5v-3h7v8h-2.5",
  close: "M3.5 3.5l9 9M12.5 3.5l-9 9",
  chevronDown: "M3.5 6l4.5 4.5L12.5 6",
  chevronLeft: "M10.5 3.5 6 8l4.5 4.5",
  hamburger: "M2 4.5h12M2 8h12M2 11.5h12",
  columns: "M2 2.5h12v11H2zM6.5 2.5v11M11 2.5v11",
} as const;

export type MiName = keyof typeof MI_PATHS;

/** Line icon — 16×16 viewBox, stroked, round caps. */
export function Mi({
  name,
  size = 16,
  color = "#323130",
  strokeWidth = 1.3,
  style,
}: {
  name: MiName;
  size?: number;
  color?: string;
  strokeWidth?: number;
  style?: CSSProperties;
}) {
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 16 16"
      fill="none"
      stroke={color}
      strokeWidth={strokeWidth}
      strokeLinecap="round"
      strokeLinejoin="round"
      style={{ flexShrink: 0, ...style }}
      aria-hidden
    >
      <path d={MI_PATHS[name]} />
    </svg>
  );
}

const FI_PATHS = {
  play: "M5.5 3.5v9l7-4.5z",
  stop: "M4.5 4.5h7v7h-7z",
} as const;

/** Filled micro-icon (play/stop) for command bars. */
export function Fi({
  name,
  size = 14,
  color = "#323130",
}: {
  name: keyof typeof FI_PATHS;
  size?: number;
  color?: string;
}) {
  return (
    <svg width={size} height={size} viewBox="0 0 16 16" style={{ flexShrink: 0 }} aria-hidden>
      <path d={FI_PATHS[name]} fill={color} />
    </svg>
  );
}

type Shape = [string, Record<string, string | number>];

const SVC_SHAPES: Record<string, Shape[]> = {
  vm: [
    ["rect", { x: 1, y: 2.5, width: 18, height: 12.5, rx: 0.8, fill: "#005BA1" }],
    ["rect", { x: 2.6, y: 4.1, width: 14.8, height: 9.3, fill: "#50E6FF" }],
    ["path", { d: "M2.6 13.4 11.5 4.1h5.9v9.3z", fill: "#0078D4" }],
    ["rect", { x: 6.5, y: 16.2, width: 7, height: 1.6, rx: 0.5, fill: "#8A8886" }],
  ],
  lxc: [
    // Container icon: stacked box in brand colors (design has no LXC glyph;
    // composed from the design's block-volume + vm language)
    ["path", { d: "M10 1.5 17.5 5.5v9L10 18.5 2.5 14.5v-9z", fill: "#005BA1" }],
    ["path", { d: "M10 1.5 17.5 5.5 10 9.5 2.5 5.5z", fill: "#50E6FF" }],
    ["path", { d: "M10 9.5v9l7.5-4v-9z", fill: "#0078D4" }],
  ],
  k8s: [
    ["path", { d: "M10 1.2 17.8 5.7v9L10 19.2 2.2 14.7v-9z", fill: "#0078D4" }],
    ["circle", { cx: 10, cy: 10, r: 2.2, fill: "#fff" }],
    ["circle", { cx: 10, cy: 4.8, r: 1.4, fill: "#50E6FF" }],
    ["circle", { cx: 5.2, cy: 12.8, r: 1.4, fill: "#50E6FF" }],
    ["circle", { cx: 14.8, cy: 12.8, r: 1.4, fill: "#50E6FF" }],
    [
      "path",
      { d: "M10 6.2v1.8M6.4 12.1l1.8-1M13.6 12.1l-1.8-1", stroke: "#C3F1FF", strokeWidth: 1.1 },
    ],
  ],
  node: [
    ["rect", { x: 2, y: 2.5, width: 16, height: 6.4, rx: 0.8, fill: "#005BA1" }],
    ["rect", { x: 2, y: 11.1, width: 16, height: 6.4, rx: 0.8, fill: "#0078D4" }],
    ["circle", { cx: 5.2, cy: 5.7, r: 1.1, fill: "#50E6FF" }],
    ["circle", { cx: 5.2, cy: 14.3, r: 1.1, fill: "#50E6FF" }],
  ],
  net: [
    ["path", { d: "M10 4.5 4 15M10 4.5 16 15M4 15h12", stroke: "#50E6FF", strokeWidth: 1.6 }],
    ["circle", { cx: 10, cy: 4, r: 2.6, fill: "#0078D4" }],
    ["circle", { cx: 4, cy: 15.5, r: 2.6, fill: "#0078D4" }],
    ["circle", { cx: 16, cy: 15.5, r: 2.6, fill: "#005BA1" }],
  ],
  vol: [
    ["rect", { x: 2.5, y: 3, width: 15, height: 4.2, rx: 1, fill: "#005BA1" }],
    ["rect", { x: 2.5, y: 8.2, width: 15, height: 4.2, rx: 1, fill: "#0078D4" }],
    ["rect", { x: 2.5, y: 13.4, width: 15, height: 4.2, rx: 1, fill: "#50E6FF" }],
  ],
  allres: [
    ["rect", { x: 2, y: 2, width: 7.2, height: 7.2, fill: "#0078D4" }],
    ["rect", { x: 10.8, y: 2, width: 7.2, height: 7.2, fill: "#50E6FF" }],
    ["rect", { x: 2, y: 10.8, width: 7.2, height: 7.2, fill: "#50E6FF" }],
    ["rect", { x: 10.8, y: 10.8, width: 7.2, height: 7.2, fill: "#005BA1" }],
  ],
};

export type SvcName = "vm" | "lxc" | "k8s" | "node" | "net" | "vol" | "allres";

/** Multicolor product/service icon — 20×20 viewBox. */
export function Svc({ name, size = 20 }: { name: SvcName; size?: number }) {
  const shapes = SVC_SHAPES[name] ?? SVC_SHAPES.allres;
  return (
    <svg width={size} height={size} viewBox="0 0 20 20" style={{ flexShrink: 0 }} aria-hidden>
      {shapes.map(([tag, attrs], i) => {
        const Tag = tag as "rect";
        return <Tag key={i} fill="none" {...attrs} />;
      })}
    </svg>
  );
}

/** Brand hexagon logo. */
export function BrandLogo({ size = 18 }: { size?: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 20 20" aria-hidden>
      <path d="M10 1.5 18 6v8l-8 4.5L2 14V6z" fill="#0078D4" />
      <path d="M10 1.5 18 6l-8 4.5L2 6z" fill="#50E6FF" />
      <path d="M10 10.5V19L2 14V6z" fill="#005BA1" />
    </svg>
  );
}

/** Fluent spinner — track ring + accent arc, 1s rotation. */
export function Spinner({ size = 16, color = "#0078D4" }: { size?: number; color?: string }) {
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 16 16"
      className="animate-pcspin"
      style={{ flexShrink: 0 }}
      aria-hidden
    >
      <circle cx={8} cy={8} r={6} fill="none" stroke="#DEECF9" strokeWidth={2} />
      <path
        d="M8 2a6 6 0 0 1 6 6"
        fill="none"
        stroke={color}
        strokeWidth={2}
        strokeLinecap="round"
      />
    </svg>
  );
}
