// Directory switcher tests: the presentational TenantPane (radio wiring for
// tenants + projects, and its loading/error states) plus the real switch flow
// (useSwitchTenant → PATCH /api/auth/active-tenant → persist active tenant +
// invalidate scoped queries). apiFetch is mocked so no network is touched.
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  act,
  cleanup,
  fireEvent,
  render,
  renderHook,
  screen,
  waitFor,
} from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { Project, TenantMembership } from "@/lib/api/generated/types";

vi.mock("@/lib/stores/toastStore", () => ({ pushToast: vi.fn() }));
vi.mock("@/lib/api/client", () => {
  class ApiError extends Error {
    code: string;
    status: number;
    constructor(status: number, body: { code: string; message: string }) {
      super(body.message);
      this.code = body.code;
      this.status = status;
    }
    get detail() {
      return this.message;
    }
  }
  return { apiFetch: vi.fn().mockResolvedValue(undefined), ApiError };
});

import { TenantPane } from "@/components/chrome/ClusterPane";
import { apiFetch } from "@/lib/api/client";
import { useSwitchTenant } from "@/lib/api/tenant";
import { useUiStore } from "@/lib/stores/uiStore";

const mockApiFetch = vi.mocked(apiFetch);

const tenants: TenantMembership[] = [
  { id: "t-acme", name: "Acme", slug: "acme", role: "owner" },
  { id: "t-globex", name: "Globex", slug: "globex", role: "reader" },
];

const projects: Project[] = [
  {
    id: "p-web",
    tenantId: "t-acme",
    name: "Web",
    slug: "web",
    poolId: "pc-acme-web",
    createdAt: "",
    updatedAt: "",
  },
  {
    id: "p-data",
    tenantId: "t-acme",
    name: "Data",
    slug: "data",
    poolId: "pc-acme-data",
    createdAt: "",
    updatedAt: "",
  },
];

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
  useUiStore.setState({ activeTenantId: null, projectFilter: null });
});

function paneProps(overrides: Record<string, unknown> = {}) {
  return {
    tenants,
    activeTenantId: "t-acme",
    onSelectTenant: vi.fn(),
    switching: false,
    projects,
    projectsPending: false,
    projectsError: null,
    selectedProjectId: null,
    onSelectProject: vi.fn(),
    onClose: vi.fn(),
    onSignOut: vi.fn(),
    ...overrides,
  };
}

describe("TenantPane — tenant selection", () => {
  it("marks the active tenant and switches on a different one", () => {
    const onSelectTenant = vi.fn();
    render(<TenantPane {...paneProps({ onSelectTenant })} />);

    const acme = screen.getByRole("radio", { name: "Directory Acme" });
    const globex = screen.getByRole("radio", { name: "Directory Globex" });
    expect(acme.getAttribute("aria-checked")).toBe("true");
    expect(globex.getAttribute("aria-checked")).toBe("false");

    fireEvent.click(globex);
    expect(onSelectTenant).toHaveBeenCalledWith("t-globex");
  });

  it("does not re-switch when the active tenant is clicked", () => {
    const onSelectTenant = vi.fn();
    render(<TenantPane {...paneProps({ onSelectTenant })} />);

    fireEvent.click(screen.getByRole("radio", { name: "Directory Acme" }));
    expect(onSelectTenant).not.toHaveBeenCalled();
  });
});

describe("TenantPane — project filter", () => {
  it("lists projects with an All-projects option and reports the selection", () => {
    const onSelectProject = vi.fn();
    render(<TenantPane {...paneProps({ onSelectProject })} />);

    expect(screen.getByRole("radio", { name: "All projects" }).getAttribute("aria-checked")).toBe(
      "true",
    );
    fireEvent.click(screen.getByRole("radio", { name: "Project Web" }));
    expect(onSelectProject).toHaveBeenCalledWith("p-web");

    fireEvent.click(screen.getByRole("radio", { name: "All projects" }));
    expect(onSelectProject).toHaveBeenCalledWith(null);
  });

  it("renders a loading state and no project rows while projects load", () => {
    const { container } = render(<TenantPane {...paneProps({ projectsPending: true })} />);
    expect(container.querySelectorAll(".animate-pulse").length).toBeGreaterThan(0);
    expect(screen.queryByRole("radio", { name: "Project Web" })).toBeNull();
  });

  it("renders the real error message on project load failure", () => {
    render(
      <TenantPane {...paneProps({ projectsError: new Error("projects store unavailable") })} />,
    );
    expect(screen.getByText("projects store unavailable")).toBeTruthy();
  });
});

describe("useSwitchTenant — switch flow", () => {
  beforeEach(() => {
    useUiStore.setState({ activeTenantId: "t-acme", projectFilter: "p-web" });
  });

  it("PATCHes the active tenant, persists it, drops the project filter, and rescopes", async () => {
    const qc = new QueryClient();
    const invalidate = vi.spyOn(qc, "invalidateQueries");
    const wrapper = ({ children }: { children: React.ReactNode }) => (
      <QueryClientProvider client={qc}>{children}</QueryClientProvider>
    );

    const { result } = renderHook(() => useSwitchTenant(), { wrapper });
    await act(async () => {
      await result.current.mutateAsync("t-globex");
    });

    expect(mockApiFetch).toHaveBeenCalledWith(
      "/api/auth/active-tenant",
      expect.objectContaining({ method: "PATCH", body: JSON.stringify({ tenantId: "t-globex" }) }),
    );
    await waitFor(() => expect(useUiStore.getState().activeTenantId).toBe("t-globex"));
    // Switching a directory must not carry the old tenant's project filter over.
    expect(useUiStore.getState().projectFilter).toBeNull();
    expect(invalidate).toHaveBeenCalled();
  });
});
