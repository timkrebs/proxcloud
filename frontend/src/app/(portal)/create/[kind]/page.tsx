"use client";
// Create wizard — design §3.3: tab strip with gating, sticky summary card,
// sticky footer. Submit posts the real CreateGuestRequest and routes to the
// deployment progress page.
import { useEffect, useMemo, useState } from "react";
import Link from "next/link";
import { useParams, useRouter } from "next/navigation";

import {
  AdvancedTab,
  BasicsTab,
  ImageTab,
  NetworkingTab,
  ReviewTab,
  SizeTab,
  TagsTab,
} from "@/components/wizard/tabs";
import { Button } from "@/components/ui/Button";
import { Card } from "@/components/ui/Card";
import { StatusDot } from "@/components/ui/StatusDot";
import { Mi } from "@/components/ui/icons";
import { ApiError } from "@/lib/api/client";
import { isQuotaExceeded, useCreateGuest } from "@/lib/api/mutations";
import { useProjectQuota } from "@/lib/api/quota";
import { usePricing, useResources } from "@/lib/api/queries";
import { useActiveTenantId } from "@/lib/stores/uiStore";
import { pushToast } from "@/lib/stores/toastStore";
import { CostRows } from "@/components/wizard/CostRows";
import {
  TAB_NAMES,
  effectiveRemaining,
  toCreateRequest,
  useWizardStore,
  validateWizard,
} from "@/lib/stores/wizardStore";

export default function WizardPage() {
  const params = useParams<{ kind: string }>();
  const kind = params.kind === "lxc" ? "lxc" : "qemu";
  const router = useRouter();
  const s = useWizardStore();
  const resources = useResources();
  const pricing = usePricing();
  const tenantId = useActiveTenantId();
  const [submitted, setSubmitted] = useState(false);
  // 409 quota_exceeded is surfaced inline (distinct from the generic conflict
  // toast) — the backend names the tightest violated dimension/scope.
  const [quotaError, setQuotaError] = useState<string | null>(null);

  useEffect(() => {
    s.init(kind);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [kind]);

  const existingVmids = useMemo(() => (resources.data ?? []).map((g) => g.vmid), [resources.data]);
  // Bind sizing validation on the tighter of project vs tenant remaining.
  const projectQuota = useProjectQuota(s.projectId || null);
  const remaining = projectQuota.data ? effectiveRemaining(projectQuota.data) : null;
  const errs = validateWizard(s, existingVmids, remaining);

  const create = useCreateGuest();

  const kindLabel = kind === "qemu" ? "virtual machine" : "LXC container";
  const onReview = s.tab === TAB_NAMES.length - 1;

  const submit = () => {
    setQuotaError(null);
    if (errs.length > 0) {
      s.set({ tab: TAB_NAMES.length - 1, maxTab: TAB_NAMES.length - 1 });
      return;
    }
    create.mutate(toCreateRequest(s), {
      onSuccess: (res) => {
        setSubmitted(true);
        router.push(`/deployments/${res.deploymentId}`);
      },
      onError: (err) => {
        if (isQuotaExceeded(err)) {
          // Send the user back to Size and show the sizing error inline.
          setQuotaError(err instanceof ApiError ? err.message : "Over quota");
          s.set({ tab: 2, maxTab: Math.max(s.maxTab, 2) });
          return;
        }
        pushToast({
          kind: "err",
          title: "Could not start the deployment",
          desc: err instanceof ApiError ? err.detail : String(err),
        });
      },
    });
  };

  return (
    <div className="max-w-[1200px] px-8 pt-5">
      <nav className="mb-[10px] text-[12px]">
        <Link href="/dashboard">Home</Link>
        <span className="text-ink-2"> &gt; </span>
        <Link href="/create">Create a resource</Link>
        <span className="text-ink-2"> &gt; </span>
        <span className="text-ink-2">Create a {kindLabel}</span>
      </nav>
      <h1 className="text-[24px] font-semibold">Create a {kindLabel}</h1>

      {/* tab strip §3.3 */}
      <div className="mt-4 flex gap-[2px] border-b border-line">
        {TAB_NAMES.map((name, i) => {
          const locked = i > s.maxTab;
          const active = i === s.tab;
          const tabErr = errs.some((e) => e.tab === i) && s.maxTab >= i && !active;
          return (
            <button
              key={name}
              type="button"
              disabled={locked}
              onClick={() => s.goTab(i)}
              className={`-mb-px cursor-pointer border-0 border-b-2 bg-transparent px-3 py-[9px] text-[14px] ${
                active
                  ? "border-accent font-semibold text-ink"
                  : locked
                    ? "cursor-default border-transparent text-ink-3"
                    : "border-transparent text-ink-2 hover:text-ink"
              }`}
            >
              {name}
              {tabErr ? <span className="ml-1 text-err">•</span> : null}
            </button>
          );
        })}
      </div>

      <div className="flex items-start gap-7 pt-5">
        <div className="max-w-[720px] flex-1 pb-24">
          {quotaError ? (
            <div className="mb-4 flex items-start gap-2 rounded-fluent border border-err bg-err-bg px-3 py-[10px] text-[13px] leading-[1.5]">
              <Mi
                name="warn"
                size={16}
                color="var(--color-err)"
                style={{ flexShrink: 0, marginTop: 1 }}
              />
              <div>
                <div className="font-semibold text-err-text">Over quota</div>
                {quotaError}
              </div>
            </div>
          ) : null}
          {s.tab === 0 ? <BasicsTab errs={errs.filter((e) => e.tab === 0)} /> : null}
          {s.tab === 1 ? <ImageTab errs={errs.filter((e) => e.tab === 1)} /> : null}
          {s.tab === 2 ? <SizeTab errs={errs.filter((e) => e.tab === 2)} /> : null}
          {s.tab === 3 ? <NetworkingTab errs={errs.filter((e) => e.tab === 3)} /> : null}
          {s.tab === 4 ? <AdvancedTab /> : null}
          {s.tab === 5 ? <TagsTab errs={errs.filter((e) => e.tab === 5)} /> : null}
          {s.tab === 6 ? <ReviewTab errs={errs} /> : null}
        </div>

        {/* sticky summary card §3.3 (pricing lands later; summary is real) */}
        <Card className="sticky top-5 w-[300px] flex-none p-4">
          <h3 className="mb-3 text-[14px] font-semibold">
            {pricing.data?.enabled ? "Estimated cost" : "Resource summary"}
          </h3>
          {[
            ["Type", kind === "qemu" ? "Virtual machine" : "LXC container"],
            ["Project", s.projectName || "—"],
            ["Compute", `${s.cores || "—"} vCPU · ${s.memoryMb || "—"} MiB`],
            ...(s.sourceMode !== "clone"
              ? [["Disk", `${s.diskGb || "—"} GiB${s.storage ? ` on ${s.storage}` : ""}`]]
              : []),
            ["Node", s.node || "—"],
          ].map(([k, v]) => (
            <div key={k} className="flex justify-between py-[3px] text-[13px]">
              <span className="text-ink-2">{k}</span>
              <span className="tabular-nums">{v}</span>
            </div>
          ))}
          {pricing.data?.enabled ? (
            <CostRows
              pricing={pricing.data}
              cores={Number(s.cores) || 0}
              memoryMb={Number(s.memoryMb) || 0}
              diskGb={s.sourceMode === "clone" ? 0 : Number(s.diskGb) || 0}
            />
          ) : null}
          <div className="my-3 h-px bg-line" />
          <div className="flex items-center gap-[6px] text-[12px] text-ink-2">
            <StatusDot status={errs.length === 0 ? "running" : "failed"} />
            {errs.length === 0
              ? "Configuration is valid"
              : `${errs.length} issue(s) to resolve before create`}
          </div>
        </Card>
      </div>

      {/* sticky footer §3.3 */}
      <div className="sticky bottom-0 z-[5] -mx-8 mt-7 flex items-center gap-2 border-t border-line bg-card px-8 py-3">
        <Button
          variant="primary"
          disabled={create.isPending || submitted || (onReview && tenantId === null)}
          onClick={onReview ? submit : () => s.set({ tab: 6, maxTab: 6 })}
        >
          {onReview ? (create.isPending ? "Creating…" : "Create") : "Review + create"}
        </Button>
        <Button variant="secondary" disabled={s.tab === 0} onClick={() => s.prev()}>
          &lt; Previous
        </Button>
        {!onReview ? (
          <Button variant="secondary" onClick={() => s.next()}>
            Next : {TAB_NAMES[s.tab + 1]} &gt;
          </Button>
        ) : null}
      </div>
    </div>
  );
}
