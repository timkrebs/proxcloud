"use client";
// The seven wizard tab bodies — design §3.3 frame with real Proxmox data
// sources in every dropdown.
import { useEffect } from "react";

import { CardError, Skeleton } from "@/components/dashboard/DashboardCards";
import { FormRow, SectionHeading } from "@/components/wizard/fields";
import { Input } from "@/components/ui/Input";
import { Select } from "@/components/ui/Select";
import { Textarea } from "@/components/ui/Textarea";
import { Toggle } from "@/components/ui/Toggle";
import { Mi } from "@/components/ui/icons";
import { useProjectQuota } from "@/lib/api/quota";
import { useResources } from "@/lib/api/queries";
import { useProjects } from "@/lib/api/tenant";
import {
  useBridges,
  useCatalogNodes,
  useNextId,
  useNodeStorages,
  useStorageContent,
} from "@/lib/api/wizardQueries";
import { formatBytes } from "@/lib/format";
import {
  SIZE_PRESETS,
  effectiveRemaining,
  useWizardStore,
  validateWizard,
  type QuotaRemaining,
  type WizardError,
} from "@/lib/stores/wizardStore";

function fieldError(errs: WizardError[], field: string): string | undefined {
  return errs.find((e) => e.field === field)?.message.replace(/ \([A-Za-z+ ]+\)\.$/, ".");
}

// ── Basics ───────────────────────────────────────────────────────────────────

function RemainingQuota({ remaining }: { remaining: QuotaRemaining }) {
  const fmt = (n: number | null, unit: (v: number) => string) =>
    n == null ? "Unlimited" : `${unit(n)} remaining`;
  const rows: [string, string][] = [
    ["vCPU", fmt(remaining.vcpu, (n) => String(n))],
    ["Memory", fmt(remaining.ramMb, (n) => formatBytes(n * 1024 * 1024))],
    ["Disk", fmt(remaining.diskGb, (n) => formatBytes(n * 1024 ** 3))],
    ["Guests", fmt(remaining.count, (n) => String(n))],
  ];
  return (
    <div className="mt-2 max-w-[420px] rounded-fluent border border-line bg-hover px-3 py-[10px]">
      <div className="mb-1 text-[12px] font-semibold text-ink">Remaining quota in this project</div>
      <div className="grid grid-cols-2 gap-x-6 gap-y-1 text-[12px] text-ink-2">
        {rows.map(([k, v]) => (
          <div key={k} className="flex justify-between">
            <span>{k}</span>
            <span className="tabular-nums">{v}</span>
          </div>
        ))}
      </div>
    </div>
  );
}

export function BasicsTab({ errs }: { errs: WizardError[] }) {
  const s = useWizardStore();
  const nodes = useCatalogNodes();
  const projects = useProjects();
  const nextId = useNextId();
  const quota = useProjectQuota(s.projectId || null);
  const kindLabel = s.kind === "qemu" ? "virtual machine" : "container";

  // Auto-fill node (single-node cluster) and next free VMID once the data
  // arrives — in an effect, never during render.
  const set = s.set;
  useEffect(() => {
    const st = useWizardStore.getState();
    if (st.node === "" && (nodes.data ?? []).length > 0) {
      set({ node: nodes.data![0].name });
    }
    if (st.vmid === "" && nextId.data) {
      set({ vmid: String(nextId.data.vmid) });
    }
  }, [nodes.data, nextId.data, set]);

  return (
    <div>
      <p className="mb-[18px] text-[13px] leading-[1.5] text-ink-2">
        Create a {kindLabel} on your Proxmox server. Complete the Basics tab, then review each tab
        or go straight to Review + create.
      </p>

      <SectionHeading caption="Every resource lives on exactly one node and belongs to a project — the resource group that scopes ownership and access.">
        Instance details
      </SectionHeading>

      <FormRow
        label={s.kind === "qemu" ? "Virtual machine name" : "Container hostname"}
        required
        help="1–40 characters: lowercase letters, digits, and hyphens. Must start with a letter."
        error={fieldError(errs, "name")}
      >
        <Input
          value={s.name}
          onChange={(e) => s.set({ name: e.target.value })}
          placeholder={s.kind === "qemu" ? "e.g. web-prod-02" : "e.g. cache-01"}
          invalid={!!fieldError(errs, "name") && s.name !== ""}
          className="w-[300px]"
        />
      </FormRow>

      <FormRow label="Target node" required error={fieldError(errs, "node")}>
        {nodes.isPending ? (
          <Skeleton className="h-8 w-[300px]" />
        ) : nodes.isError ? (
          <CardError err={nodes.error} />
        ) : (
          <Select
            value={s.node}
            onChange={(e) => s.set({ node: e.target.value, storage: "", bridge: "" })}
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

      <FormRow
        label="Project"
        required
        help="The resource group the new guest is created into; its pool and ownership derive from this."
        error={fieldError(errs, "projectId")}
      >
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
            value={s.projectId}
            invalid={!!fieldError(errs, "projectId")}
            onChange={(e) => {
              const id = e.target.value;
              const name = (projects.data ?? []).find((p) => p.id === id)?.name ?? "";
              s.set({ projectId: id, projectName: name });
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
        {s.projectId ? (
          quota.isPending ? (
            <Skeleton className="mt-2 h-16 w-[420px]" />
          ) : quota.isError ? (
            <div className="mt-2">
              <CardError err={quota.error} />
            </div>
          ) : quota.data ? (
            <RemainingQuota remaining={effectiveRemaining(quota.data)} />
          ) : null
        ) : null}
      </FormRow>

      <FormRow
        label="VMID"
        required
        help="Auto-suggested from the cluster's next free ID."
        error={fieldError(errs, "vmid")}
      >
        <Input
          value={s.vmid}
          onChange={(e) => s.set({ vmid: e.target.value })}
          className="w-[300px]"
        />
      </FormRow>
    </div>
  );
}

// ── Image ────────────────────────────────────────────────────────────────────

export function ImageTab({ errs }: { errs: WizardError[] }) {
  const s = useWizardStore();
  const isoStorages = useNodeStorages(s.node, "iso");
  const vzStorages = useNodeStorages(s.node, "vztmpl");
  const templates = useResources({ type: "qemu" });

  if (s.kind === "lxc") {
    return (
      <div>
        <SectionHeading caption="Container templates stored on this node. Download more via the Proxmox UI (pveam) — this list is live.">
          Container template
        </SectionHeading>
        <TemplatePicker
          storagesQuery={vzStorages}
          content="vztmpl"
          value={s.vztmplVolId}
          onChange={(v) => s.set({ vztmplVolId: v })}
          error={fieldError(errs, "vztmplVolId")}
        />
      </div>
    );
  }

  const cloneSources = (templates.data ?? []).filter((g) => g.template);

  return (
    <div>
      <SectionHeading caption="Install from an ISO image, or clone an existing VM template (full or linked).">
        Source
      </SectionHeading>

      <FormRow label="Source type" required>
        <Select
          value={s.sourceMode}
          onChange={(e) => s.set({ sourceMode: e.target.value as "iso" | "clone" })}
          className="w-[300px]"
        >
          <option value="iso">Install from ISO</option>
          <option value="clone" disabled={cloneSources.length === 0}>
            Clone a template{cloneSources.length === 0 ? " (no templates on this cluster)" : ""}
          </option>
        </Select>
      </FormRow>

      {s.sourceMode === "iso" ? (
        <TemplatePicker
          storagesQuery={isoStorages}
          content="iso"
          value={s.isoVolId}
          onChange={(v) => s.set({ isoVolId: v })}
          error={fieldError(errs, "isoVolId")}
        />
      ) : (
        <>
          <FormRow label="Template" required error={fieldError(errs, "cloneVmid")}>
            <Select
              value={s.cloneVmid ? String(s.cloneVmid) : ""}
              onChange={(e) => s.set({ cloneVmid: e.target.value ? Number(e.target.value) : null })}
              className="w-[300px]"
            >
              <option value="">Select a template…</option>
              {cloneSources.map((t) => (
                <option key={t.vmid} value={t.vmid}>
                  {t.name} (VMID {t.vmid})
                </option>
              ))}
            </Select>
          </FormRow>
          <FormRow
            label="Clone mode"
            help="Linked clones are instant and space-efficient but must stay on the template's storage; full clones are independent copies."
          >
            <Select
              value={s.cloneMode}
              onChange={(e) => s.set({ cloneMode: e.target.value as "full" | "linked" })}
              className="w-[300px]"
            >
              <option value="full">Full clone</option>
              <option value="linked">Linked clone</option>
            </Select>
          </FormRow>
        </>
      )}
    </div>
  );
}

function TemplatePicker({
  storagesQuery,
  content,
  value,
  onChange,
  error,
}: {
  storagesQuery: ReturnType<typeof useNodeStorages>;
  content: "iso" | "vztmpl";
  value: string;
  onChange: (v: string) => void;
  error?: string;
}) {
  const s = useWizardStore();
  const storages = storagesQuery.data ?? [];
  const firstStorage = storages[0]?.storage ?? "";
  const [selStorage, items] = [firstStorage, useStorageContent(s.node, firstStorage, content)];

  // Collect content across all storages that hold this content type; the
  // common homelab has one, so one query keeps it simple — additional
  // storages are listed with a hint.
  if (storagesQuery.isPending) return <Skeleton className="h-8 w-[420px]" />;
  if (storagesQuery.isError) return <CardError err={storagesQuery.error} />;
  if (storages.length === 0) {
    return (
      <p className="text-[13px] text-ink-2">
        No storage on {s.node} offers {content === "iso" ? "ISO images" : "container templates"}.
      </p>
    );
  }

  return (
    <FormRow label={content === "iso" ? "ISO image" : "Template"} required error={error}>
      {items.isPending ? (
        <Skeleton className="h-8 w-[420px]" />
      ) : items.isError ? (
        <CardError err={items.error} />
      ) : (items.data ?? []).length === 0 ? (
        <p className="text-[13px] text-ink-2">
          Storage {selStorage} has no {content === "iso" ? "ISO images" : "templates"} yet — upload
          one in the Proxmox UI.
        </p>
      ) : (
        <Select value={value} onChange={(e) => onChange(e.target.value)} className="w-[420px]">
          <option value="">Select…</option>
          {(items.data ?? []).map((i) => (
            <option key={i.volid} value={i.volid}>
              {i.volid.split("/").pop()} ({formatBytes(i.sizeBytes, 1)})
            </option>
          ))}
        </Select>
      )}
      {storages.length > 1 ? (
        <p className="mt-1 text-[12px] text-ink-3">
          Showing storage {selStorage}; other storages also hold {content} content.
        </p>
      ) : null}
    </FormRow>
  );
}

// ── Size ─────────────────────────────────────────────────────────────────────

export function SizeTab({ errs }: { errs: WizardError[] }) {
  const s = useWizardStore();
  const storages = useNodeStorages(s.node, s.kind === "lxc" ? "rootdir" : "images");
  const clone = s.sourceMode === "clone";
  const active = SIZE_PRESETS.find(
    (p) => String(p.cores) === s.cores && String(p.ramGiB * 1024) === s.memoryMb,
  );

  const quotaErr = errs.find((e) => e.field === "quota")?.message;

  return (
    <div>
      <SectionHeading caption="T-shirt sizes prefill cores and RAM — adjust the exact values below. You can resize later.">
        Size
      </SectionHeading>

      {quotaErr ? (
        <div className="mb-4 flex items-start gap-2 rounded-fluent border border-err bg-err-bg px-3 py-[10px] text-[13px] leading-[1.5] text-err-text">
          <Mi
            name="warn"
            size={16}
            color="var(--color-err)"
            style={{ flexShrink: 0, marginTop: 1 }}
          />
          {quotaErr}
        </div>
      ) : null}

      <div className="mb-5 grid max-w-[720px] grid-cols-[repeat(auto-fill,minmax(160px,1fr))] gap-[10px]">
        {SIZE_PRESETS.map((p) => {
          const selected = active?.name === p.name;
          return (
            <button
              key={p.name}
              type="button"
              onClick={() => s.set({ cores: String(p.cores), memoryMb: String(p.ramGiB * 1024) })}
              className={`cursor-pointer rounded-fluent p-[14px] text-left ${
                selected
                  ? "border border-accent bg-selected shadow-[inset_0_0_0_1px_var(--color-accent)]"
                  : "border border-line-soft bg-card hover:border-accent"
              }`}
            >
              <div className="flex items-start justify-between">
                <span className="text-[16px] font-semibold">{p.name}</span>
                {selected ? <Mi name="checkC" size={16} color="var(--color-accent)" /> : null}
              </div>
              <div className="text-[13px]">
                {p.cores} vCPU · {p.ramGiB} GiB RAM
              </div>
            </button>
          );
        })}
      </div>

      <FormRow label="Cores" required error={fieldError(errs, "cores")}>
        <Input
          value={s.cores}
          onChange={(e) => s.set({ cores: e.target.value })}
          className="w-[300px]"
        />
      </FormRow>
      <FormRow label="Memory (MiB)" required error={fieldError(errs, "memoryMb")}>
        <Input
          value={s.memoryMb}
          onChange={(e) => s.set({ memoryMb: e.target.value })}
          className="w-[300px]"
        />
      </FormRow>

      {clone ? (
        <p className="text-[13px] text-ink-2">
          Disks are copied from the template.{" "}
          {s.cloneMode === "full"
            ? "Choose the target storage below."
            : "Linked clones stay on the template's storage."}
        </p>
      ) : (
        <FormRow
          label={s.kind === "lxc" ? "Root disk (GiB)" : "OS disk (GiB)"}
          required
          error={fieldError(errs, "diskGb")}
        >
          <Input
            value={s.diskGb}
            onChange={(e) => s.set({ diskGb: e.target.value })}
            className="w-[300px]"
          />
        </FormRow>
      )}

      {!clone || s.cloneMode === "full" ? (
        <FormRow label="Storage pool" required={!clone} error={fieldError(errs, "storage")}>
          {storages.isPending ? (
            <Skeleton className="h-8 w-[300px]" />
          ) : storages.isError ? (
            <CardError err={storages.error} />
          ) : (
            <Select
              value={s.storage}
              onChange={(e) => s.set({ storage: e.target.value })}
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
      ) : null}
    </div>
  );
}

// ── Networking ───────────────────────────────────────────────────────────────

export function NetworkingTab({ errs }: { errs: WizardError[] }) {
  const s = useWizardStore();
  const bridges = useBridges(s.node);
  const clone = s.sourceMode === "clone";

  if (clone) {
    return (
      <p className="text-[13px] leading-[1.5] text-ink-2">
        The clone keeps the template&apos;s network configuration. Adjust it on the guest&apos;s
        Networking blade after creation.
      </p>
    );
  }

  return (
    <div>
      <SectionHeading caption="The guest gets one NIC on the selected bridge.">
        Network interface
      </SectionHeading>

      <FormRow label="Bridge" required error={fieldError(errs, "bridge")}>
        {bridges.isPending ? (
          <Skeleton className="h-8 w-[300px]" />
        ) : bridges.isError ? (
          <CardError err={bridges.error} />
        ) : (
          <Select
            value={s.bridge}
            onChange={(e) => s.set({ bridge: e.target.value })}
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
          value={s.vlanTag}
          onChange={(e) => s.set({ vlanTag: e.target.value })}
          placeholder="none"
          className="w-[300px]"
        />
      </FormRow>

      <FormRow label="Proxmox firewall" help="Enables the PVE firewall on this NIC.">
        <div className="flex h-8 items-center gap-2">
          <Toggle
            checked={s.firewall}
            onChange={(on) => s.set({ firewall: on })}
            aria-label="Firewall"
          />
          <span className="text-[13px] text-ink-2">
            {s.firewall ? "Enabled on the NIC" : "Disabled"}
          </span>
        </div>
      </FormRow>

      <SectionHeading
        caption={
          s.kind === "qemu"
            ? "Applied via cloud-init on first boot (requires a cloud-init-ready image)."
            : "Applied to the container's eth0."
        }
      >
        IP configuration
      </SectionHeading>

      <FormRow label="Assignment" required>
        <Select
          value={s.ipMode}
          onChange={(e) => s.set({ ipMode: e.target.value as "dhcp" | "static" })}
          className="w-[300px]"
        >
          <option value="dhcp">DHCP</option>
          <option value="static">Static</option>
        </Select>
      </FormRow>

      {s.ipMode === "static" ? (
        <>
          <FormRow label="Address (CIDR)" required error={fieldError(errs, "cidr")}>
            <Input
              value={s.cidr}
              onChange={(e) => s.set({ cidr: e.target.value })}
              placeholder="192.168.1.50/24"
              className="w-[300px]"
            />
          </FormRow>
          <FormRow label="Gateway" error={fieldError(errs, "gateway")}>
            <Input
              value={s.gateway}
              onChange={(e) => s.set({ gateway: e.target.value })}
              placeholder="192.168.1.1"
              className="w-[300px]"
            />
          </FormRow>
        </>
      ) : null}
    </div>
  );
}

// ── Advanced ─────────────────────────────────────────────────────────────────

export function AdvancedTab() {
  const s = useWizardStore();
  const qemu = s.kind === "qemu";

  if (s.sourceMode === "clone") {
    return (
      <p className="text-[13px] leading-[1.5] text-ink-2">
        The clone keeps the template&apos;s cloud-init settings.
      </p>
    );
  }

  return (
    <div>
      <SectionHeading
        caption={
          qemu
            ? "Cloud-init account settings, injected on first boot. Custom user-data files are not supported in v1."
            : "Provisioning settings applied when the container is created."
        }
      >
        {qemu ? "Cloud-init" : "Provisioning"}
      </SectionHeading>

      {qemu ? (
        <FormRow label="Default user" help="cloud-init ciuser">
          <Input
            value={s.ciUser}
            onChange={(e) => s.set({ ciUser: e.target.value })}
            placeholder="e.g. admin"
            className="w-[300px]"
          />
        </FormRow>
      ) : null}

      <FormRow label={qemu ? "Password" : "Root password"}>
        <Input
          type="password"
          value={s.ciPassword}
          onChange={(e) => s.set({ ciPassword: e.target.value })}
          className="w-[300px]"
        />
      </FormRow>

      <FormRow label="SSH public keys" help="One key per line; injected on first boot.">
        <Textarea
          value={s.sshKeys}
          onChange={(e) => s.set({ sshKeys: e.target.value })}
          rows={5}
          placeholder="ssh-ed25519 AAAA…"
          className="w-[420px]"
        />
      </FormRow>

      <FormRow label="DNS server" help="Optional nameserver override.">
        <Input
          value={s.nameserver}
          onChange={(e) => s.set({ nameserver: e.target.value })}
          placeholder="e.g. 1.1.1.1"
          className="w-[300px]"
        />
      </FormRow>

      <FormRow label="Start after create">
        <div className="flex h-8 items-center gap-2">
          <Toggle
            checked={s.startAfterCreate}
            onChange={(on) => s.set({ startAfterCreate: on })}
            aria-label="Start after create"
          />
          <span className="text-[13px] text-ink-2">
            {s.startAfterCreate
              ? "The guest starts as soon as creation finishes"
              : "Created stopped"}
          </span>
        </div>
      </FormRow>
    </div>
  );
}

// ── Tags ─────────────────────────────────────────────────────────────────────

export function TagsTab({ errs }: { errs: WizardError[] }) {
  const s = useWizardStore();
  return (
    <div>
      <SectionHeading caption="Proxmox tags are flat labels (lowercase letters, digits, . - _) for organizing and filtering.">
        Tags
      </SectionHeading>
      <TagEditor
        tags={s.tags}
        onChange={(tags) => s.set({ tags })}
        error={fieldError(errs, "tags")}
      />
    </div>
  );
}

function TagEditor({
  tags,
  onChange,
  error,
}: {
  tags: string[];
  onChange: (t: string[]) => void;
  error?: string;
}) {
  return (
    <div>
      <div className="mb-3 flex flex-wrap gap-2">
        {tags.length === 0 ? <span className="text-[13px] text-ink-2">No tags yet.</span> : null}
        {tags.map((t) => (
          <span
            key={t}
            className="flex items-center gap-2 rounded-fluent border border-line bg-hover px-[10px] py-1 text-[13px]"
          >
            {t}
            <button
              type="button"
              title="Remove tag"
              className="cursor-pointer border-none bg-transparent p-0 text-ink-2 hover:text-err"
              onClick={() => onChange(tags.filter((x) => x !== t))}
            >
              <Mi name="close" size={11} color="currentColor" />
            </button>
          </span>
        ))}
      </div>
      <TagInput onAdd={(t) => !tags.includes(t) && onChange([...tags, t])} />
      {error ? <p className="mt-2 text-[12px] text-err-text">{error}</p> : null}
    </div>
  );
}

function TagInput({ onAdd }: { onAdd: (t: string) => void }) {
  const s = useWizardStore();
  void s; // keep hook ordering stable if store fields are added later
  return (
    <input
      placeholder="Type a tag and press Enter"
      aria-label="Add tag"
      className="h-8 w-[220px] rounded-fluent border border-line-input bg-card px-2 text-[14px] outline-none focus:border-accent"
      onKeyDown={(e) => {
        if (e.key === "Enter") {
          const v = (e.target as HTMLInputElement).value.trim();
          if (v) {
            onAdd(v);
            (e.target as HTMLInputElement).value = "";
          }
        }
      }}
    />
  );
}

// ── Review ───────────────────────────────────────────────────────────────────

export function ReviewTab({ errs }: { errs: WizardError[] }) {
  const s = useWizardStore();
  const valid = errs.length === 0;

  const groups: { title: string; rows: [string, string][] }[] = [
    {
      title: "Basics",
      rows: [
        ["Name", s.name || "—"],
        ["Type", s.kind === "qemu" ? "Virtual machine" : "LXC container"],
        ["Node", s.node || "—"],
        ["VMID", s.vmid || "—"],
        ["Project", s.projectName || "—"],
      ],
    },
    {
      title: "Image",
      rows: [
        s.kind === "lxc"
          ? ["Template", s.vztmplVolId.split("/").pop() || "—"]
          : s.sourceMode === "clone"
            ? ["Source", `Clone of VMID ${s.cloneVmid ?? "—"} (${s.cloneMode})`]
            : ["ISO", s.isoVolId.split("/").pop() || "—"],
      ],
    },
    {
      title: "Size",
      rows: [
        ["Cores", s.cores],
        ["Memory", `${s.memoryMb} MiB`],
        ...(s.sourceMode !== "clone"
          ? ([["Disk", `${s.diskGb} GiB on ${s.storage || "—"}`]] as [string, string][])
          : []),
      ],
    },
    ...(s.sourceMode !== "clone"
      ? [
          {
            title: "Networking",
            rows: [
              ["Bridge", s.bridge || "—"],
              ["VLAN tag", s.vlanTag || "none"],
              ["Firewall", s.firewall ? "Enabled" : "Disabled"],
              [
                "IP",
                s.ipMode === "static" ? `${s.cidr}${s.gateway ? ` via ${s.gateway}` : ""}` : "DHCP",
              ],
            ] as [string, string][],
          },
        ]
      : []),
    {
      title: "Advanced",
      rows: [
        ["Provisioning", s.ciUser || s.ciPassword || s.sshKeys.trim() ? "Configured" : "—"],
        ["Start after create", s.startAfterCreate ? "Yes" : "No"],
      ],
    },
    { title: "Tags", rows: [["Tags", s.tags.join(", ") || "—"]] },
  ];

  return (
    <div>
      {valid ? (
        <div className="mb-[18px] flex items-center gap-2 rounded-fluent border border-ok bg-ok-bg px-3 py-[10px] text-[13px]">
          <Mi name="checkC" size={16} color="var(--color-ok)" />
          <span className="font-semibold text-ok">Validation passed</span>
        </div>
      ) : (
        <div className="mb-[18px] rounded-fluent border border-err bg-err-bg px-3 py-[10px] text-[13px]">
          <div className="flex items-center gap-2 font-semibold text-err-text">
            <Mi name="warn" size={16} color="var(--color-err)" />
            Validation failed — fix the following before creating
          </div>
          <ul className="mt-2 ml-6 list-disc text-ink">
            {errs.map((e, i) => (
              <li key={i}>{e.message}</li>
            ))}
          </ul>
        </div>
      )}

      {groups.map((grp) => (
        <div key={grp.title} className="mb-4">
          <h3 className="text-[14px] font-semibold">{grp.title}</h3>
          <div className="mt-[6px] mb-2 h-px bg-line" />
          {grp.rows.map(([k, v]) => (
            <div key={k} className="flex py-1 text-[13px]">
              <span className="w-[220px] flex-none text-ink-2">{k}</span>
              <span>{v}</span>
            </div>
          ))}
        </div>
      ))}
    </div>
  );
}

export { validateWizard };
