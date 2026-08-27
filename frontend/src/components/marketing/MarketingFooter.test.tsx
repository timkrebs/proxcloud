// Theme toggle: flips data-theme on the marketing root wrapper, updates the
// button's accessible label, and persists the choice to localStorage.
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeAll, beforeEach, describe, expect, it } from "vitest";

import { MarketingFooter } from "./MarketingFooter";
import { MarketingRoot } from "./MarketingRoot";

function makeStorage(): Storage {
  const map = new Map<string, string>();
  return {
    get length() {
      return map.size;
    },
    clear: () => map.clear(),
    getItem: (k: string) => map.get(k) ?? null,
    key: (i: number) => Array.from(map.keys())[i] ?? null,
    removeItem: (k: string) => void map.delete(k),
    setItem: (k: string, v: string) => void map.set(k, String(v)),
  };
}

beforeAll(() => {
  // Default system preference = light so the initial resolved theme is "light".
  window.matchMedia = ((query: string) => ({
    matches: false,
    media: query,
    onchange: null,
    addListener: () => {},
    removeListener: () => {},
    addEventListener: () => {},
    removeEventListener: () => {},
    dispatchEvent: () => false,
  })) as typeof window.matchMedia;
  // jsdom in this runtime does not wire up localStorage; provide an in-memory one.
  Object.defineProperty(window, "localStorage", { value: makeStorage(), configurable: true });
});

beforeEach(() => window.localStorage.clear());
afterEach(() => cleanup());

function root() {
  return document.querySelector(".pc-marketing") as HTMLElement;
}

describe("theme toggle", () => {
  it("starts in light mode", async () => {
    render(
      <MarketingRoot>
        <MarketingFooter />
      </MarketingRoot>,
    );
    await waitFor(() => expect(root().getAttribute("data-theme")).toBe("light"));
    expect(screen.getByRole("button", { name: "Switch to dark theme" })).toBeTruthy();
  });

  it("toggles data-theme to dark and persists it", async () => {
    render(
      <MarketingRoot>
        <MarketingFooter />
      </MarketingRoot>,
    );
    await waitFor(() => expect(root().getAttribute("data-theme")).toBe("light"));

    fireEvent.click(screen.getByRole("button", { name: "Switch to dark theme" }));

    await waitFor(() => expect(root().getAttribute("data-theme")).toBe("dark"));
    expect(window.localStorage.getItem("pc-theme")).toBe("dark");
    expect(screen.getByRole("button", { name: "Switch to light theme" })).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: "Switch to light theme" }));
    await waitFor(() => expect(root().getAttribute("data-theme")).toBe("light"));
    expect(window.localStorage.getItem("pc-theme")).toBe("light");
  });
});
