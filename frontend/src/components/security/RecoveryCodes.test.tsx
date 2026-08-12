// RecoveryCodes reveal — the one-time recovery codes are shown once and the
// "Done" dismissal is gated behind an explicit "I've saved these" acknowledgement
// so a user can't accidentally throw away their only copy. Pure presentational —
// no query client, no network.
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { RecoveryCodes } from "@/components/security/RecoveryCodes";

afterEach(() => cleanup());

const CODES = ["AAAAA-11111", "BBBBB-22222", "CCCCC-33333"];

describe("RecoveryCodes display gate", () => {
  it("renders every code exactly once", () => {
    render(<RecoveryCodes codes={CODES} onDone={vi.fn()} />);
    for (const c of CODES) {
      expect(screen.getByText(c)).toBeTruthy();
    }
  });

  it("keeps Done disabled and inert until the save is acknowledged", () => {
    const onDone = vi.fn();
    render(<RecoveryCodes codes={CODES} onDone={onDone} />);

    const done = screen.getByRole("button", { name: "Done" });
    expect(done.hasAttribute("disabled")).toBe(true);
    // Clicking the disabled gate must not dismiss the only copy.
    fireEvent.click(done);
    expect(onDone).not.toHaveBeenCalled();
  });

  it("enables Done once the acknowledgement is checked, then fires onDone", () => {
    const onDone = vi.fn();
    render(<RecoveryCodes codes={CODES} onDone={onDone} />);

    const ack = screen.getByRole("checkbox", { name: "I've saved these recovery codes" });
    fireEvent.click(ack);

    const done = screen.getByRole("button", { name: "Done" });
    expect(done.hasAttribute("disabled")).toBe(false);
    fireEvent.click(done);
    expect(onDone).toHaveBeenCalledTimes(1);
  });

  it("honors a custom done label", () => {
    render(<RecoveryCodes codes={CODES} onDone={vi.fn()} doneLabel="Finish" />);
    expect(screen.getByRole("button", { name: "Finish" })).toBeTruthy();
  });

  it("offers copy and download affordances", () => {
    render(<RecoveryCodes codes={CODES} onDone={vi.fn()} />);
    expect(screen.getByRole("button", { name: "Copy codes" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Download .txt" })).toBeTruthy();
  });
});
