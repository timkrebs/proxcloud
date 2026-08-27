// Reveal progressive-enhancement + reduced-motion path. Children are always in
// the DOM (server renders them visible). With motion allowed the wrapper arms
// (`pc-reveal`) and registers an IntersectionObserver; with
// prefers-reduced-motion: reduce it must NOT arm and must NOT observe — content
// stays plainly visible, honoring the a11y contract in globals.css / ADR-0021.
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { Reveal } from "./Reveal";

/** Install a matchMedia stub whose reduced-motion answer we control. */
function stubMatchMedia(reduceMotion: boolean) {
  window.matchMedia = ((query: string) => ({
    matches: query.includes("prefers-reduced-motion") ? reduceMotion : false,
    media: query,
    onchange: null,
    addListener: () => {},
    removeListener: () => {},
    addEventListener: () => {},
    removeEventListener: () => {},
    dispatchEvent: () => false,
  })) as typeof window.matchMedia;
}

/** Install an IntersectionObserver spy; returns the observe mock. */
function stubIntersectionObserver() {
  const observe = vi.fn();
  const disconnect = vi.fn();
  class IO {
    observe = observe;
    unobserve = () => {};
    disconnect = disconnect;
    takeRecords = () => [];
    readonly root = null;
    readonly rootMargin = "";
    readonly thresholds = [];
  }
  vi.stubGlobal("IntersectionObserver", IO as unknown as typeof IntersectionObserver);
  return { observe };
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("Reveal", () => {
  it("arms the reveal and observes when motion is allowed", () => {
    stubMatchMedia(false);
    const { observe } = stubIntersectionObserver();

    render(
      <Reveal className="feature">
        <p>Revealed content</p>
      </Reveal>,
    );

    // Content is always present (progressive enhancement).
    const child = screen.getByText("Revealed content");
    const wrapper = child.parentElement!;
    expect(wrapper.className).toContain("pc-reveal");
    expect(wrapper.className).toContain("feature");
    expect(observe).toHaveBeenCalledTimes(1);
  });

  it("does NOT arm or observe under prefers-reduced-motion: reduce", () => {
    stubMatchMedia(true);
    const { observe } = stubIntersectionObserver();

    render(
      <Reveal className="feature">
        <p>Revealed content</p>
      </Reveal>,
    );

    const child = screen.getByText("Revealed content");
    const wrapper = child.parentElement!;
    // Stays plainly visible: no reveal animation class, no observer registered.
    expect(wrapper.className).not.toContain("pc-reveal");
    expect(wrapper.className).toContain("feature");
    expect(observe).not.toHaveBeenCalled();
  });
});
