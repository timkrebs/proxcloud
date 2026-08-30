"use client";
// Create a deployment set (ADR-0029/0030) — a dedicated form (NOT the single-guest
// wizard): a set provisions N linked members (one K3s `server` control plane plus
// `agent` workers) sharing a lifecycle. It collects name/project/node, the STATIC
// control-plane serverIp (CIDR + gateway), the worker count (clamped to the agent
// role's [min,max]), the disk storage + bridge, at least one SSH key, and the VMID
// block (server + one per agent). The request shaping/validation is the pure,
// unit-tested deploymentSetForm module; the server re-validates authoritatively and
// a 409 (quota/conflict) routes back inline. On 202 we go to the set detail page.
import { Suspense, useEffect, useMemo, useRef, useState } from "react";
import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";

import { CardError, Skeleton } from "@/components/dashboard/DashboardCards";
import { FormRow, SectionHeading } from "@/components/wizard/fields";
import { Button } from "@/components/ui/Button";
import { Card } from "@/components/ui/Card";
import { Input } from "@/components/ui/Input";
import { Select } from "@/components/ui/Select";
import { StatusDot } from "@/components/ui/StatusDot";
import { Textarea } from "@/components/ui/Textarea";
import { Toggle } from "@/components/ui/Toggle";
import { Mi, Svc } from "@/components/ui/icons";
import { ApiError } from "@/lib/api/client";
import { useCreateSet } from "@/lib/api/deploymentSets";
import { isCatalogDisabled, useService } from "@/lib/api/serviceCatalog";
import { isQuotaExceeded } from "@/lib/api/mutations";
import { useProjectQuota } from "@/lib/api/quota";
import { useResources } from "@/lib/api/queries";
import { useProjects } from "@/lib/api/tenant";
import { useBridges, useCatalogNodes, useNextId, useNodeStorages } from "@/lib/api/wizardQueries";
import { useActiveTenantId } from "@/lib/stores/uiStore";
import { pushToast } from "@/lib/stores/toastStore";
import { effectiveRemaining, type QuotaRemaining } from "@/lib/stores/wizardStore";
import { formatBytes } from "@/lib/format";
import {
  agentBounds,
  allocateSetVmids,
  clampAgentCount,
  emptySetForm,
  findRole,
  parseSshKeys,
  setTotals,
  toCreateSetRequest,
  validateSetForm,
  type AgentBounds,
  type SetFormError,
  type SetFormState,
  type SetTotals,
} from "@/lib/deploymentSetForm";

function fieldError(errs: SetFormError[], field: string): string | undefined {
  return errs.find((e) => e.field === field)?.message;
}

/** Client-side over-quota feedback on the SUM of all members (server authoritative). */
function quotaErrors(totals: SetTotals, remaining: QuotaRemaining | null): SetFormError[] {
  if (!remaining) return [];
  const errs: SetFormError[] = [];
  if (remaining.count != null && totals.count > remaining.count) {
    errs.push({
      field: "quota",
      message: `This cluster needs ${totals.count} guests but only ${remaining.count} remain in the project's quota.`,
    });
  }
  if (remaining.vcpu != null && totals.vcpu > remaining.vcpu) {
    errs.push({
      field: "quota",
      message: `This cluster needs ${totals.vcpu} vCPU but only ${remaining.vcpu} remain in the project's quota.`,
    });
  }
  if (remaining.ramMb != null && totals.ramMb > remaining.ramMb) {
    errs.push({
      field: "quota",
      message: `This cluster needs ${totals.ramMb} MiB of memory but only ${remaining.ramMb} remain in the project's quota.`,
    });
  }
  if (remaining.diskGb != null && totals.diskGb > remaining.diskGb) {
    errs.push({
      field: "quota",
      message: `This cluster needs ${totals.diskGb} GiB of disk but only ${remaining.diskGb} remain in the project's quota.`,
    });
  }
  return errs;
}

function SetCreateInner() {
  const searchParams = useSearchParams();
  const serviceId = searchParams.get("service") ?? "";
  const router = useRouter();
  const tenantId = useActiveTenantId();

  const service = useService(serviceId || null);
  const svc = service.data;

  const [form, setForm] = useState<SetFormState>(() => emptySetForm(serviceId));
  const patch = (p: Partial<SetFormState>) => setForm((f) => ({ ...f, ...p }));
  const [quotaError, setQuotaError] = useState<string | null>(null);
  const [submitted, setSubmitted] = useState(false);

  // Data sources — the real cluster via the tenant catalog projection.
  const projects = useProjects();
  const nodes = useCatalogNodes();
  const nextId = useNextId();
  const storages = useNodeStorages(form.node, "images");
  const bridges = useBridges(form.node);
  const resources = useResources();
  const projectQuota = useProjectQuota(form.projectId || null);

  const bounds: AgentBounds = useMemo(
    () => agentBounds(svc ? findRole(svc, "agent") : undefined),
    [svc],
  );
  const serverRole = svc ? findRole(svc, "server") : undefined;
  const agentRole = svc ? findRole(svc, "agent") : undefined;

  // Prefill once the service def loads: name, the default worker count, and the
  // start VMID from the cluster's next free id. Guarded so it never clobbers edits.
  const prefilledRef = useRef<string>("");
  useEffect(() => {
    if (!svc) return;
    if (prefilledRef.current === svc.id) return;
    prefilledRef.current = svc.id;
    patch({
      serviceId: svc.id,
      name: svc.id,
      agentCount: agentBounds(findRole(svc, "agent")).default,
    });
  }, [svc]);

  // Auto-fill node + start VMID once the cluster data arrives (effect, not render).
  useEffect(() => {
    if (form.node === "" && (nodes.data ?? []).length > 0) patch({ node: nodes.data![0].name });
    if (form.startVmid === "" && nextId.data) patch({ startVmid: String(nextId.data.vmid) });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [nodes.data, nextId.data]);

  const taken = useMemo(() => new Set((resources.data ?? []).map((g) => g.vmid)), [resources.data]);
  const clampedCount = svc ? clampAgentCount(form.agentCount, bounds) : form.agentCount;
  const allocation = useMemo(() => {
    const start = Number(form.startVmid);
    if (!Number.isInteger(start)) return { serverVmid: 0, agentVmids: [] as number[] };
    return allocateSetVmids(start, clampedCount, taken);
  }, [form.startVmid, clampedCount, taken]);

  const totals = svc ? setTotals(svc, clampedCount) : { vcpu: 0, ramMb: 0, diskGb: 0, count: 0 };
  const remaining = projectQuota.data ? effectiveRemaining(projectQuota.data) : null;

  const baseErrs = svc ? validateSetForm(form, bounds, taken) : [];
  const qErrs = quotaErrors(totals, remaining);
  const errs = [...baseErrs, ...qErrs];

  const create = useCreateSet();
  const pending = create.isPending;

  // Service loading / disabled / error states.
  if (!serviceId) {
    return (
      <div className="m-8 max-w-[720px]">
        <p className="text-[13px] text-ink-2">
          No service selected. <Link href="/create">Browse the catalog</Link> and pick a cluster
          service.
        </p>
      </div>
    );
  }
  if (service.isPending) return <Skeleton className="m-8 h-64 max-w-[900px]" />;
  if (service.isError) {
    if (isCatalogDisabled(service.error)) {
      return (
        <div className="m-8 max-w-[720px]">
          <p className="text-[13px] text-ink-2">
            The service catalog is not enabled. <Link href="/create">Back to Create</Link>.
          </p>
        </div>
      );
    }
    return (
      <div className="m-8 max-w-[720px]">
        <CardError err={service.error} />
      </div>
    );
  }
  if (!svc) return <Skeleton className="m-8 h-64 max-w-[900px]" />;

  if (svc.kind !== "set") {
    return (
      <div className="m-8 max-w-[720px]">
        <p className="text-[13px] text-ink-2">
          {svc.displayName} is a single-guest service.{" "}
          <Link href={`/create/${svc.guestType === "lxc" ? "lxc" : "vm"}?service=${svc.id}`}>
            Open the guest wizard
          </Link>
          .
        </p>
      </div>
    );
  }

  const submit = () => {
    setQuotaError(null);
    if (errs.length > 0) return; // the inline field errors are already visible
    create.mutate(toCreateSetRequest(form, bounds, taken), {
      onSuccess: (res) => {
        setSubmitted(true);
        router.push(`/deployment-sets/${res.setId}`);
      },
      onError: (err) => {
        if (isQuotaExceeded(err) || (err instanceof ApiError && err.code === "conflict")) {
          setQuotaError(
            err instanceof ApiError ? err.message : "The cluster could not be reserved.",
          );
          return;
        }
        pushToast({
          kind: "err",
          title: "Could not start the cluster",
          desc: err instanceof ApiError ? err.detail : String(err),
        });
      },
    });
  };

  const canSubmit = !pending && !submitted && tenantId !== null && errs.length === 0;

  return (
    <div className="max-w-[1200px] px-8 pt-5 pb-10">
      <nav className="mb-[10px] text-[12px]">
        <Link href="/dashboard">Home</Link>
        <span className="text-ink-2"> &gt; </span>
        <Link href="/create">Create a resource</Link>
        <span className="text-ink-2"> &gt; </span>
        <span className="text-ink-2">Create {svc.displayName}</span>
      </nav>
      <h1 className="text-[24px] font-semibold">Create {svc.displayName}</h1>

      <div className="mt-3 flex max-w-[720px] items-start gap-2 rounded-fluent border border-line bg-hover px-3 py-[10px] text-[13px] leading-[1.5]">
        <Mi
          name="info"
          size={16}
          color="var(--color-accent)"
          style={{ flexShrink: 0, marginTop: 1 }}
        />
        <div>
          This provisions a cluster: one control plane plus your chosen number of workers, sharing a
          lifecycle. The join token is generated server-side and <strong>never shown</strong> —
          fetch the kubeconfig from the control plane once it is ready.
        </div>
      </div>

      <div className="flex items-start gap-7 pt-5">
        <div className="max-w-[720px] flex-1">
          {quotaError ? (
            <div className="mb-4 flex items-start gap-2 rounded-fluent border border-err bg-err-bg px-3 py-[10px] text-[13px] leading-[1.5]">
              <Mi
                name="warn"
                size={16}
                color="var(--color-err)"
                style={{ flexShrink: 0, marginTop: 1 }}
              />
              <div>
                <div className="font-semibold text-err-text">Could not reserve the cluster</div>
                {quotaError}
              </div>
            </div>
          ) : null}

          {/* ── Cluster basics ── */}
          <SectionHeading caption="The set's base name — members become <name>-server and <name>-agent-N.">
            Cluster basics
          </SectionHeading>

          <FormRow
            label="Cluster name"
            required
            help="1–40 characters: lowercase letters, digits, and hyphens. Must start with a letter."
            error={fieldError(errs, "name")}
          >
            <Input
              value={form.name}
              onChange={(e) => patch({ name: e.target.value })}
              placeholder="e.g. k3s-prod"
              invalid={!!fieldError(errs, "name") && form.name !== ""}
              className="w-[300px]"
            />
          </FormRow>

          <FormRow label="Project" required error={fieldError(errs, "projectId")}>
            {projects.isPending ? (
              <Skeleton className="h-8 w-[300px]" />
            ) : projects.isError ? (
              <CardError err={projects.error} />
            ) : (projects.data ?? []).length === 0 ? (
              <p className="text-[13px] text-ink-2">
                No projects in this directory yet — create one from the Projects screen first.
              </p>
            ) : (
              <Select
                value={form.projectId}
                invalid={!!fieldError(errs, "projectId")}
                onChange={(e) => {
                  const id = e.target.value;
                  const nm = (projects.data ?? []).find((p) => p.id === id)?.name ?? "";
                  patch({ projectId: id, projectName: nm });
                }}
                className="w-[300px]"
              >
                <option value="">Select a project…</option>
                {(projects.data ?? []).map((p) => (
                  <option key={p.id} value={p.id}>
                    {p.name}
                  </option>
                ))}
              </Select>
            )}
          </FormRow>

          <FormRow label="Target node" required error={fieldError(errs, "node")}>
            {nodes.isPending ? (
              <Skeleton className="h-8 w-[300px]" />
            ) : nodes.isError ? (
              <CardError err={nodes.error} />
            ) : (
              <Select
                value={form.node}
                onChange={(e) => patch({ node: e.target.value, storage: "", bridge: "" })}
                className="w-[300px]"
              >
                {(nodes.data ?? []).map((n) => (
                  <option key={n.name} value={n.name}>
                    {n.name}
                  </option>
                ))}
              </Select>
            )}
          </FormRow>

          {/* ── Topology ── */}
          <SectionHeading caption="One control-plane server is always created; choose how many workers join it.">
            Topology
          </SectionHeading>

          <FormRow
            label="Workers"
            required
            help={`Between ${bounds.min} and ${bounds.max} agent nodes.`}
            error={fieldError(errs, "agentCount")}
          >
            <div className="flex items-center gap-3">
              <input
                type="range"
                min={bounds.min}
                max={bounds.max}
                value={form.agentCount}
                onChange={(e) => patch({ agentCount: Number(e.target.value) })}
                aria-label="Worker count"
                className="w-[220px]"
              />
              <Input
                value={String(form.agentCount)}
                onChange={(e) => patch({ agentCount: Number(e.target.value) || 0 })}
                className="w-[70px] text-center"
                aria-label="Worker count value"
              />
            </div>
            <p className="mt-1 text-[12px] text-ink-2">
              Total: 1 server + {clampedCount} agent{clampedCount === 1 ? "" : "s"} ={" "}
              {clampedCount + 1} guests.
            </p>
          </FormRow>

          <FormRow
            label="Starting VMID"
            required
            help="The server takes this id; agents take the next free ids after it."
            error={fieldError(errs, "startVmid")}
          >
            <Input
              value={form.startVmid}
              onChange={(e) => patch({ startVmid: e.target.value })}
              className="w-[300px]"
              invalid={!!fieldError(errs, "startVmid")}
            />
            {allocation.serverVmid > 0 ? (
              <p className="mt-1 text-[12px] text-ink-2">
                Server VMID {allocation.serverVmid} · Agents{" "}
                {allocation.agentVmids.length > 0 ? allocation.agentVmids.join(", ") : "—"}
              </p>
            ) : null}
          </FormRow>

          {/* ── Networking ── */}
          <SectionHeading caption="The control plane needs a static, joinable address; workers use DHCP.">
            Control-plane network
          </SectionHeading>

          <FormRow label="Control-plane IP (CIDR)" required error={fieldError(errs, "cidr")}>
            <Input
              value={form.cidr}
              onChange={(e) => patch({ cidr: e.target.value })}
              placeholder="192.168.1.50/24"
              invalid={!!fieldError(errs, "cidr")}
              className="w-[300px]"
            />
          </FormRow>
          <FormRow label="Gateway" required error={fieldError(errs, "gateway")}>
            <Input
              value={form.gateway}
              onChange={(e) => patch({ gateway: e.target.value })}
              placeholder="192.168.1.1"
              invalid={!!fieldError(errs, "gateway")}
              className="w-[300px]"
            />
          </FormRow>

          <FormRow label="Bridge" required error={fieldError(errs, "bridge")}>
            {bridges.isPending ? (
              <Skeleton className="h-8 w-[300px]" />
            ) : bridges.isError ? (
              <CardError err={bridges.error} />
            ) : (
              <Select
                value={form.bridge}
                onChange={(e) => patch({ bridge: e.target.value })}
                invalid={!!fieldError(errs, "bridge")}
                className="w-[300px]"
              >
                <option value="">Select…</option>
                {(bridges.data ?? []).map((b) => (
                  <option key={b.iface} value={b.iface}>
                    {b.iface}
                    {b.comment ? ` — ${b.comment}` : ""}
                  </option>
                ))}
              </Select>
            )}
          </FormRow>

          <FormRow
            label="VLAN tag"
            help="Optional 802.1q tag (1–4094)."
            error={fieldError(errs, "vlanTag")}
          >
            <Input
              value={form.vlanTag}
              onChange={(e) => patch({ vlanTag: e.target.value })}
              placeholder="none"
              className="w-[300px]"
            />
          </FormRow>

          <FormRow label="Proxmox firewall" help="Enables the PVE firewall on each member NIC.">
            <div className="flex h-8 items-center gap-2">
              <Toggle
                checked={form.firewall}
                onChange={(on) => patch({ firewall: on })}
                aria-label="Firewall"
              />
              <span className="text-[13px] text-ink-2">
                {form.firewall ? "Enabled on member NICs" : "Disabled"}
              </span>
            </div>
          </FormRow>

          {/* ── Storage ── */}
          <SectionHeading caption="Where each member's OS disk lives.">Storage</SectionHeading>
          <FormRow label="Storage pool" required error={fieldError(errs, "storage")}>
            {storages.isPending ? (
              <Skeleton className="h-8 w-[300px]" />
            ) : storages.isError ? (
              <CardError err={storages.error} />
            ) : (
              <Select
                value={form.storage}
                onChange={(e) => patch({ storage: e.target.value })}
                invalid={!!fieldError(errs, "storage")}
                className="w-[300px]"
              >
                <option value="">Select…</option>
                {(storages.data ?? []).map((st) => (
                  <option key={st.storage} value={st.storage}>
                    {st.storage} ({st.type})
                  </option>
                ))}
              </Select>
            )}
          </FormRow>

          {/* ── Access ── */}
          <SectionHeading caption="Cluster nodes lock password login — at least one SSH public key is required.">
            Access
          </SectionHeading>
          <FormRow label="SSH public keys" required error={fieldError(errs, "sshKeys")}>
            <Textarea
              value={form.sshKeys}
              onChange={(e) => patch({ sshKeys: e.target.value })}
              placeholder="ssh-ed25519 AAAA… user@host&#10;(one key per line)"
              rows={4}
              invalid={!!fieldError(errs, "sshKeys")}
              className="w-[420px]"
            />
            <p className="mt-1 text-[12px] text-ink-2">
              {parseSshKeys(form.sshKeys).length} key(s). Applied to every member.
            </p>
          </FormRow>

          <FormRow
            label="Tags"
            help="Optional. Comma- or space-separated."
            error={fieldError(errs, "tags")}
          >
            <Input
              value={form.tags.join(" ")}
              onChange={(e) =>
                patch({
                  tags: e.target.value
                    .split(/[\s,]+/)
                    .map((t) => t.trim())
                    .filter(Boolean),
                })
              }
              placeholder="e.g. k3s prod"
              className="w-[300px]"
            />
          </FormRow>

          {/* ── Quota feedback ── */}
          {qErrs.length > 0 ? (
            <div className="mt-4 rounded-fluent border border-err bg-err-bg px-3 py-[10px] text-[13px] leading-[1.5] text-err-text">
              <div className="mb-1 flex items-center gap-2 font-semibold">
                <Mi name="warn" size={16} color="var(--color-err)" />
                This cluster exceeds the project quota
              </div>
              <ul className="ml-[18px] list-disc">
                {qErrs.map((e, i) => (
                  <li key={i}>{e.message}</li>
                ))}
              </ul>
            </div>
          ) : null}

          <div className="mt-6 flex items-center gap-2">
            <Button variant="primary" disabled={!canSubmit} onClick={submit}>
              {pending ? "Creating…" : "Create cluster"}
            </Button>
            <Link href="/create">
              <Button variant="secondary">Cancel</Button>
            </Link>
          </div>
        </div>

        {/* Sticky summary card. */}
        <Card className="sticky top-5 w-[300px] flex-none p-4">
          <div className="mb-3 flex items-center gap-2">
            <Svc name="k8s" size={20} />
            <h3 className="text-[14px] font-semibold">Cluster summary</h3>
          </div>
          {(
            [
              ["Service", svc.displayName],
              ["Project", form.projectName || "—"],
              ["Node", form.node || "—"],
              ["Topology", `1 server + ${clampedCount} agents`],
              [
                "Server size",
                serverRole
                  ? `${serverRole.sizing.default.cores} vCPU · ${serverRole.sizing.default.memoryMb} MiB`
                  : "—",
              ],
              [
                "Agent size",
                agentRole
                  ? `${agentRole.sizing.default.cores} vCPU · ${agentRole.sizing.default.memoryMb} MiB`
                  : "—",
              ],
              ["Total vCPU", String(totals.vcpu)],
              ["Total memory", formatBytes(totals.ramMb * 1024 * 1024)],
              ["Total disk", formatBytes(totals.diskGb * 1024 ** 3)],
            ] as [string, string][]
          ).map(([k, v]) => (
            <div key={k} className="flex justify-between py-[3px] text-[13px]">
              <span className="text-ink-2">{k}</span>
              <span className="text-right tabular-nums">{v}</span>
            </div>
          ))}
          <div className="my-3 h-px bg-line" />
          <div className="flex items-center gap-[6px] text-[12px] text-ink-2">
            <StatusDot status={errs.length === 0 ? "running" : "failed"} />
            {errs.length === 0
              ? "Configuration is valid"
              : `${errs.length} issue(s) to resolve before create`}
          </div>
        </Card>
      </div>
    </div>
  );
}

export default function SetCreatePage() {
  return (
    <Suspense fallback={null}>
      <SetCreateInner />
    </Suspense>
  );
}
