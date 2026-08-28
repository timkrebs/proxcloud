"use client";
// Create wizard — design §3.3: tab strip with gating, sticky summary card,
// sticky footer. Submit posts the real CreateGuestRequest (bare VM/LXC) or, in
// service-catalog mode (?service=<id>), a ProvisionServiceRequest — the service
// def prefills name/sizing and defines the base image, and the one-time
// generated credential is surfaced once before routing to the deployment page.
import { Suspense, useEffect, useMemo, useRef, useState } from "react";
import Link from "next/link";
import { useParams, useRouter, useSearchParams } from "next/navigation";

import {
  AdvancedTab,
  BasicsTab,
  ImageTab,
  NetworkingTab,
  ReviewTab,
  SizeTab,
  TagsTab,
} from "@/components/wizard/tabs";
import { CredentialReveal } from "@/components/catalog/CredentialReveal";
import { Button } from "@/components/ui/Button";
import { Card } from "@/components/ui/Card";
import { StatusDot } from "@/components/ui/StatusDot";
import { Mi } from "@/components/ui/icons";
import { ApiError } from "@/lib/api/client";
import type { ProvisionServiceResponse } from "@/lib/api/generated/types";
import { isQuotaExceeded, useCreateGuest } from "@/lib/api/mutations";
import { useProjectQuota } from "@/lib/api/quota";
import { usePricing, useResources } from "@/lib/api/queries";
import { useProvisionService, useService } from "@/lib/api/serviceCatalog";
import { useActiveTenantId } from "@/lib/stores/uiStore";
import { pushToast } from "@/lib/stores/toastStore";
import { CostRows } from "@/components/wizard/CostRows";
import {
  TAB_NAMES,
  effectiveRemaining,
  tabIndex,
  toCreateRequest,
  toProvisionRequest,
  useWizardStore,
  validateWizard,
} from "@/lib/stores/wizardStore";

function WizardPageInner() {
  const params = useParams<{ kind: string }>();
  const kind = params.kind === "lxc" ? "lxc" : "qemu";
  const searchParams = useSearchParams();
  const serviceId = searchParams.get("service");
  const inServiceMode = serviceId !== null;
  const router = useRouter();
  const s = useWizardStore();
  const resources = useResources();
  const pricing = usePricing();
  const tenantId = useActiveTenantId();
  const [submitted, setSubmitted] = useState(false);
  // 409 quota_exceeded is surfaced inline (distinct from the generic conflict
  // toast) — the backend names the tightest violated dimension/scope.
  const [quotaError, setQuotaError] = useState<string | null>(null);
  // One-time generated credential from a catalog provision — shown once here,
  // then we route to the deployment page (which never sees it again).
  const [credential, setCredential] = useState<ProvisionServiceResponse | null>(null);

  const service = useService(serviceId);
  const svc = service.data;

  useEffect(() => {
    s.init(kind);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [kind]);

  // Prefill from the service def once it loads (after init). Keyed by kind+id so
  // switching services (or kind) re-applies; guarded so it never clobbers the
  // user's later edits on a re-render.
  const set = s.set;
  const prefilledRef = useRef<string>("");
  useEffect(() => {
    if (!svc) return;
    const key = `${kind}:${svc.id}`;
    if (prefilledRef.current === key) return;
    prefilledRef.current = key;
    set({
      serviceId: svc.id,
      serviceName: svc.displayName,
      name: svc.id,
      sourceMode: "iso",
      cores: String(svc.sizing.default.cores),
      memoryMb: String(svc.sizing.default.memoryMb),
      diskGb: String(svc.sizing.default.diskGb),
    });
  }, [svc, kind, set]);

  const existingVmids = useMemo(() => (resources.data ?? []).map((g) => g.vmid), [resources.data]);
  // Bind sizing validation on the tighter of project vs tenant remaining.
  const projectQuota = useProjectQuota(s.projectId || null);
  const remaining = projectQuota.data ? effectiveRemaining(projectQuota.data) : null;
  const errs = validateWizard(s, existingVmids, remaining);

  const create = useCreateGuest();
  const provision = useProvisionService(serviceId ?? "");
  const pending = create.isPending || provision.isPending;

  const kindLabel = kind === "qemu" ? "virtual machine" : "LXC container";
  const heading = inServiceMode && svc ? `Create ${svc.displayName}` : `Create a ${kindLabel}`;
  const onReview = s.tab === tabIndex("review");

  const submit = () => {
    setQuotaError(null);
    if (errs.length > 0) {
      s.set({ tab: tabIndex("review"), maxTab: tabIndex("review") });
      return;
    }
    const onError = (err: unknown) => {
      if (isQuotaExceeded(err)) {
        // Send the user back to Size and show the sizing error inline.
        setQuotaError(err instanceof ApiError ? err.message : "Over quota");
        s.set({ tab: tabIndex("size"), maxTab: Math.max(s.maxTab, tabIndex("size")) });
        return;
      }
      pushToast({
        kind: "err",
        title: "Could not start the deployment",
        desc: err instanceof ApiError ? err.detail : String(err),
      });
    };

    if (inServiceMode) {
      provision.mutate(toProvisionRequest(s), {
        onSuccess: (res) => {
          setSubmitted(true);
          // Surface the generated password once, THEN route. If the (Phase C)
          // user-supplied path returned no password, route straight through.
          if (res.generatedPassword) setCredential(res);
          else router.push(`/deployments/${res.deploymentId}`);
        },
        onError,
      });
      return;
    }
    create.mutate(toCreateRequest(s), {
      onSuccess: (res) => {
        setSubmitted(true);
        router.push(`/deployments/${res.deploymentId}`);
      },
      onError,
    });
  };

  return (
    <div className="max-w-[1200px] px-8 pt-5">
      <nav className="mb-[10px] text-[12px]">
        <Link href="/dashboard">Home</Link>
        <span className="text-ink-2"> &gt; </span>
        <Link href="/create">Create a resource</Link>
        <span className="text-ink-2"> &gt; </span>
        <span className="text-ink-2">{heading}</span>
      </nav>
      <h1 className="text-[24px] font-semibold">{heading}</h1>

      {inServiceMode ? (
        <div className="mt-3 flex max-w-[720px] items-start gap-2 rounded-fluent border border-line bg-hover px-3 py-[10px] text-[13px] leading-[1.5]">
          <Mi
            name="info"
            size={16}
            color="var(--color-accent)"
            style={{ flexShrink: 0, marginTop: 1 }}
          />
          <div>
            This service uses a predefined base image. A superuser credential is generated
            automatically and shown <strong>once</strong> after you create it — Proxcloud does not
            store it.
          </div>
        </div>
      ) : null}

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
            ...(inServiceMode ? [["Service", s.serviceName || svc?.displayName || "—"]] : []),
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
          disabled={pending || submitted || (onReview && tenantId === null)}
          onClick={
            onReview ? submit : () => s.set({ tab: tabIndex("review"), maxTab: tabIndex("review") })
          }
        >
          {onReview ? (pending ? "Creating…" : "Create") : "Review + create"}
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

      {credential ? (
        <CredentialReveal
          resp={credential}
          onContinue={() => router.push(`/deployments/${credential.deploymentId}`)}
        />
      ) : null}
    </div>
  );
}

export default function WizardPage() {
  return (
    <Suspense fallback={null}>
      <WizardPageInner />
    </Suspense>
  );
}
