"use client";
// Deployment progress — design §3.4: spinner→check header, per-step table
// (Pending → Creating → Created), real task log tail on failure.
import Link from "next/link";
import { useParams } from "next/navigation";
import { useQuery } from "@tanstack/react-query";

import { CardError, Skeleton } from "@/components/dashboard/DashboardCards";
import { Button } from "@/components/ui/Button";
import { Card } from "@/components/ui/Card";
import { Mi, Spinner } from "@/components/ui/icons";
import { apiFetch } from "@/lib/api/client";
import type { Deployment, TaskLog } from "@/lib/api/generated/types";
import { useActiveTenantId } from "@/lib/stores/uiStore";

function StepIcon({ status }: { status: string }) {
  if (status === "running") return <Spinner size={15} />;
  if (status === "succeeded") return <Mi name="checkC" size={15} color="var(--color-ok)" />;
  if (status === "failed") return <Mi name="warn" size={15} color="var(--color-err)" />;
  return <Mi name="clock" size={15} color="var(--color-ink-3)" />;
}

const STEP_LABEL: Record<string, string> = {
  pending: "Pending",
  running: "Creating",
  succeeded: "Created",
  failed: "Failed",
};

export default function DeploymentPage() {
  const { id } = useParams<{ id: string }>();
  const tenantId = useActiveTenantId();
  const dep = useQuery({
    queryKey: ["deployment", id],
    queryFn: () => apiFetch<Deployment>(`/api/tenants/${tenantId}/deployments/${id}`),
    refetchInterval: (q) => (q.state.data?.status === "running" ? 2000 : false),
    enabled: tenantId !== null,
  });

  const failedStep = (dep.data?.steps ?? []).find((s) => s.status === "failed");
  const failLog = useQuery({
    queryKey: ["task", failedStep?.upid ?? "", "log"],
    queryFn: () =>
      apiFetch<TaskLog>(
        `/api/tenants/${tenantId}/tasks/${encodeURIComponent(failedStep!.upid!)}/log?limit=50`,
      ),
    enabled: !!failedStep?.upid && tenantId !== null,
  });

  if (dep.isPending) return <Skeleton className="m-8 h-40 max-w-[900px]" />;
  if (dep.isError)
    return (
      <div className="m-8 max-w-[900px]">
        <CardError err={dep.error} />
      </div>
    );

  const d = dep.data;
  const running = d.status === "running";
  const failed = d.status === "failed";
  const guestHref = `/resources/${d.node}/${d.type}/${d.vmid}`;

  return (
    <div className="max-w-[900px] px-8 pt-5 pb-10">
      <nav className="mb-[10px] text-[12px]">
        <Link href="/dashboard">Home</Link>
        <span className="text-ink-2"> &gt; </span>
        <span className="text-ink-2">deploy-{d.name}</span>
      </nav>

      <div className="mt-2 mb-1 flex items-center gap-[14px]">
        {running ? (
          <Spinner size={34} />
        ) : failed ? (
          <Mi name="warn" size={34} color="var(--color-err)" strokeWidth={1.1} />
        ) : (
          <Mi name="checkC" size={34} color="var(--color-ok)" strokeWidth={1.1} />
        )}
        <h1 className="text-[22px] font-semibold">
          {running
            ? "Deployment is in progress"
            : failed
              ? "Deployment failed"
              : "Your deployment is complete"}
        </h1>
      </div>
      <p className="text-[13px] text-ink-2">
        Deployment name: deploy-{d.name} · Node: {d.node} · VMID: {d.vmid}
      </p>

      <Card className="mt-5">
        <h3 className="px-4 pt-[14px] pb-[10px] text-[14px] font-semibold">Deployment details</h3>
        <table className="w-full border-collapse text-[13px]">
          <thead>
            <tr>
              {["Step", "Status", "Detail"].map((h) => (
                <th key={h} className="border-b border-line px-4 py-[6px] text-left font-semibold">
                  {h}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {d.steps.map((s) => (
              <tr key={s.key} className="border-b border-line-row last:border-b-0">
                <td className="flex h-10 items-center gap-2 px-4">
                  <StepIcon status={s.status} />
                  {s.label}
                </td>
                <td
                  className="h-10 px-4"
                  style={{
                    color:
                      s.status === "succeeded"
                        ? "var(--color-ok)"
                        : s.status === "failed"
                          ? "var(--color-err)"
                          : s.status === "running"
                            ? "var(--color-accent)"
                            : "var(--color-ink-2)",
                  }}
                >
                  {STEP_LABEL[s.status] ?? s.status}
                </td>
                <td className="h-10 px-4 text-ink-2">
                  {s.status === "failed" && s.message ? (
                    <span className="text-err-text">{s.message}</span>
                  ) : s.upid ? (
                    <span className="font-mono text-[11px] break-all">{s.upid}</span>
                  ) : (
                    "—"
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </Card>

      {failedStep && failLog.data && failLog.data.lines.length > 0 ? (
        <Card className="mt-4 p-4">
          <h3 className="mb-2 text-[14px] font-semibold">Task log (from Proxmox)</h3>
          <pre className="max-h-64 overflow-auto rounded-fluent border border-line bg-hover p-3 font-mono text-[12px] leading-[1.5] whitespace-pre-wrap">
            {failLog.data.lines.map((l) => l.t).join("\n")}
          </pre>
        </Card>
      ) : null}

      <div className="mt-5 flex items-center gap-2">
        {!running && !failed ? (
          <>
            <Link href={guestHref}>
              <Button variant="primary">Go to resource</Button>
            </Link>
            <Link href={`/create/${d.type === "qemu" ? "vm" : "lxc"}`}>
              <Button variant="secondary">Create another</Button>
            </Link>
          </>
        ) : null}
        {failed ? (
          <Link href={`/create/${d.type === "qemu" ? "vm" : "lxc"}`}>
            <Button variant="secondary">Back to the wizard</Button>
          </Link>
        ) : null}
      </div>
      <p className="mt-3 text-[12px] text-ink-2">
        You can safely leave this page — the task itself lives on in the activity log and the
        notification bell.
      </p>
    </div>
  );
}
