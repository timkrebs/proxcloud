// Header interactivity: the Products mega-menu opens/closes, closes on Escape
// with focus returned to its button, moves focus into the menu on open, and the
// mobile hamburger toggles its menu.
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeAll, describe, expect, it } from "vitest";

import { MarketingHeader } from "./MarketingHeader";
import { MarketingRoot } from "./MarketingRoot";

beforeAll(() => {
  // jsdom lacks matchMedia; MarketingRoot reads it at mount.
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
});

afterEach(() => cleanup());

function renderHeader() {
  return render(
    <MarketingRoot>
      <MarketingHeader />
    </MarketingRoot>,
  );
}

describe("Products mega-menu", () => {
  it("is closed by default", () => {
    renderHeader();
    const button = screen.getByRole("button", { name: "Products" });
    expect(button.getAttribute("aria-expanded")).toBe("false");
  });

  it("opens on click, marks aria-expanded, and moves focus into the menu", async () => {
    renderHeader();
    const button = screen.getByRole("button", { name: "Products" });
    fireEvent.click(button);
    expect(button.getAttribute("aria-expanded")).toBe("true");

    const menu = document.getElementById(button.getAttribute("aria-controls")!);
    expect(menu).not.toBeNull();
    const firstLink = within(menu!).getAllByRole("link")[0];
    await waitFor(() => expect(document.activeElement).toBe(firstLink));
  });

  it("closes on Escape and returns focus to the button", async () => {
    renderHeader();
    const button = screen.getByRole("button", { name: "Products" });
    fireEvent.click(button);
    const menu = document.getElementById(button.getAttribute("aria-controls")!)!;

    fireEvent.keyDown(menu, { key: "Escape" });

    await waitFor(() => expect(button.getAttribute("aria-expanded")).toBe("false"));
    expect(document.activeElement).toBe(button);
    expect(document.getElementById(button.getAttribute("aria-controls")!)).toBeNull();
  });

  it("toggles closed on a second click", () => {
    renderHeader();
    const button = screen.getByRole("button", { name: "Products" });
    fireEvent.click(button);
    expect(button.getAttribute("aria-expanded")).toBe("true");
    fireEvent.click(button);
    expect(button.getAttribute("aria-expanded")).toBe("false");
  });
});

describe("mobile hamburger", () => {
  it("toggles the mobile menu open and closed", () => {
    renderHeader();
    const menuButton = screen.getByRole("button", { name: "Menu" });
    expect(menuButton.getAttribute("aria-expanded")).toBe("false");
    // Mobile-only link not present while closed.
    expect(screen.queryByRole("link", { name: "Sign in to the portal" })).toBeNull();

    fireEvent.click(menuButton);
    expect(menuButton.getAttribute("aria-expanded")).toBe("true");
    expect(screen.getByRole("link", { name: "Sign in to the portal" })).toBeTruthy();

    fireEvent.click(menuButton);
    expect(menuButton.getAttribute("aria-expanded")).toBe("false");
    expect(screen.queryByRole("link", { name: "Sign in to the portal" })).toBeNull();
  });
});
