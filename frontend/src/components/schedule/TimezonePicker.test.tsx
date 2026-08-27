// TimezonePicker — options come from Intl.supportedValuesOf (fallback list on
// older runtimes), the selected value is preserved even if unknown, and changes
// propagate through onChange.
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import {
  TimezonePicker,
  resolveDefaultTimezone,
  timezoneOptions,
} from "@/components/schedule/TimezonePicker";

afterEach(() => cleanup());

describe("timezoneOptions", () => {
  it("returns a non-empty list that includes UTC", () => {
    const zones = timezoneOptions();
    expect(zones.length).toBeGreaterThan(0);
    expect(zones).toContain("UTC");
  });
});

describe("resolveDefaultTimezone", () => {
  it("resolves to a non-empty IANA-style string", () => {
    expect(resolveDefaultTimezone().length).toBeGreaterThan(0);
  });
});

describe("TimezonePicker", () => {
  it("shows the selected zone and preserves an unknown value", () => {
    render(<TimezonePicker value="Custom/Zone" onChange={() => {}} />);
    const select = screen.getByLabelText("Time zone") as HTMLSelectElement;
    expect(select.value).toBe("Custom/Zone");
    // The unknown value is prepended so it is never silently dropped.
    expect(select.options[0].value).toBe("Custom/Zone");
  });

  it("emits the chosen zone", () => {
    const onChange = vi.fn();
    render(<TimezonePicker value="UTC" onChange={onChange} />);
    const select = screen.getByLabelText("Time zone") as HTMLSelectElement;
    fireEvent.change(select, { target: { value: "UTC" } });
    expect(onChange).toHaveBeenCalledWith("UTC");
  });
});
