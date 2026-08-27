// Code block: switching tabs swaps the snippet body; Copy writes the active
// snippet to the clipboard and surfaces a confirmation toast.
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";

import { CodeBlock } from "./CodeBlock";
import { MarketingRoot } from "./MarketingRoot";

const writeText = vi.fn().mockResolvedValue(undefined);

beforeAll(() => {
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
  Object.defineProperty(navigator, "clipboard", {
    value: { writeText },
    configurable: true,
  });
});

afterEach(() => {
  cleanup();
  writeText.mockClear();
});

function renderBlock() {
  return render(
    <MarketingRoot>
      <CodeBlock />
    </MarketingRoot>,
  );
}

describe("CodeBlock tabs", () => {
  it("shows the pcctl snippet by default", () => {
    renderBlock();
    expect(screen.getByRole("tabpanel").textContent).toContain("pcctl login");
  });

  it("switches to the Terraform and REST snippets", () => {
    renderBlock();
    fireEvent.click(screen.getByRole("tab", { name: "Terraform" }));
    expect(screen.getByRole("tab", { name: "Terraform" }).getAttribute("aria-selected")).toBe("true");
    expect(screen.getByRole("tabpanel").textContent).toContain("required_providers");

    fireEvent.click(screen.getByRole("tab", { name: "REST" }));
    expect(screen.getByRole("tabpanel").textContent).toContain("HTTP/1.1 202 Accepted");
  });
});

describe("CodeBlock copy", () => {
  it("copies the active snippet and shows a toast", async () => {
    renderBlock();
    fireEvent.click(screen.getByRole("tab", { name: "Terraform" }));
    fireEvent.click(screen.getByRole("button", { name: "Copy snippet" }));

    await waitFor(() => expect(writeText).toHaveBeenCalledTimes(1));
    expect(writeText.mock.calls[0][0]).toContain("required_providers");
    await waitFor(() =>
      expect(screen.getByText("Copied the Terraform snippet.")).toBeTruthy(),
    );
  });
});
