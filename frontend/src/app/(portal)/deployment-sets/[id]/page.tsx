"use client";
// Deployment-set detail (ADR-0029/0030) — the cluster card: aggregated set
// status, a per-member table (name / role / status / VMID / blade link), and
// set-level Start / Stop / Delete. Polls while the set is provisioning/deleting
// (mirroring deployments/[id]) AND is seeded live by the deployment_set SSE frame
// (sse.ts). Delete is typed-name confirm-guarded and warns to Stop first when
// members are still running. A failed set keeps its successful members RUNNING
// and quota-charged until deleted — the page says so. The join token is NEVER
// rendered (the API never returns it); the next-steps surface kubeconfig retrieval.
import { useState } from "react";
import Link from "next/link";
import { useParams, useRouter } from "next/navigation";

import { CardError, Skeleton } from "@/components/dashboard/DashboardCards";
import { Button } from "@/components/ui/Button";
import { Card } from "@/components/ui/Card";
import { Flyout } from "@/components/ui/Flyout";
import { Input } from "@/components/ui/Input";
import { StatusPill } from "@/components/ui/StatusPill";
import { Mi, Spinner, Svc } from "@/components/ui/icons";
import type { DeploymentSet } from "@/lib/api/generated/types";
import {
  isSetsDisabled,
  useDeleteSet,
  useDeploymentSet,
  useSetAction,
} from "@/lib/api/deploymentSets";
import { useService } from "@/lib/api/serviceCatalog";
import {
  hasLiveMembers,
  isSetTransitional,
  memberLabel,
  orderedMembers,
  serverMember,
  setBaseName,
} from "@/lib/deploymentSetView";
import { formatDateTime } from "@/lib/format";

const MEMBER_STATUS_LABEL: Record<string, string> = {
  pending: "Pending",
  provisioning: "Provisioning",
  active: "Active",
  failed: "Failed",
  tombstoned: "Removed",
};

const SET_HEADLINE: Record<string, string> = {
  provisioning: "Cluster is provisioning",
  ready: "Cluster is ready",
  degraded: "Cluster is degraded",
  failed: "Cluster provisioning failed",
  deleting: "Cluster is being deleted",
};

export default function DeploymentSetPage() {
  const { id } = useParams<{ id: string }>();
  const router = useRouter();
  const set = useDeploymentSet(id);
  const [confirmDelete, setConfirmDelete] = useState(false);

  if (set.isPending) return <Skeleton className="m-8 h-56 max-w-[900px]" />;
  if (set.isError) {
    // A disabled (404) feature reads as "not found" here — route back to the list,
    // which renders the disabled/empty state.
    if (isSetsDisabled(set.error)) {
      return (
        <div className="m-8 max-w-[900px]">
          <p className="text-[13px] text-ink-2">
            This deployment set is not available.{" "}
            <Link href="/deployment-sets">Back to deployment sets</Link>.
          </p>
        </div>
      );
    }
    return (
      <div className="m-8 max-w-[900px]">
        <CardError err={set.error} />
      </div>
    );
  }

  const d = set.data;
  return (
    <SetDetail
      set={d}
      onDelete={() => setConfirmDelete(true)}
      confirmDelete={confirmDelete}
      onCloseDelete={() => setConfirmDelete(false)}
      onDeleted={() => router.push("/deployment-sets")}
    />
  );
}

function SetDetail({
  set,
  onDelete,
  confirmDelete,
  onCloseDelete,
  onDeleted,
}: {
  set: DeploymentSet;
  onDelete: () => void;
  confirmDelete: boolean;
  onCloseDelete: () => void;
  onDeleted: () => void;
}) {
  const svc = useService(set.serviceId);
  const action = useSetAction();
  const name = setBaseName(set);
  const transitional = isSetTransitional(set.status);
  const provisioning = set.status === "provisioning";
  const failed = set.status === "failed";
  const ready = set.status === "ready";
  const live = hasLiveMembers(set);
  const members = orderedMembers(set);
  const server = serverMember(set);
  const serviceLabel = svc.data?.displayName ?? set.serviceId;

  return (
    <div className="max-w-[900px] px-8 pt-5 pb-10">
      <nav className="mb-[10px] text-[12px]">
        <Link href="/dashboard">Home</Link>
        <span className="text-ink-2"> &gt; </span>
        <Link href="/deployment-sets">Deployment sets</Link>
        <span className="text-ink-2"> &gt; </span>
        <span className="text-ink-2">{name}</span>
      </nav>

      <div className="mt-2 mb-1 flex items-center gap-[14px]">
        {provisioning ? (
          <Spinner size={30} />
        ) : failed ? (
          <Mi name="warn" size={30} color="var(--color-err)" strokeWidth={1.1} />
        ) : ready ? (
          <Mi name="checkC" size={30} color="var(--color-ok)" strokeWidth={1.1} />
        ) : (
          <Svc name="k8s" size={30} />
        )}
        <h1 className="text-[22px] font-semibold">{SET_HEADLINE[set.status] ?? name}</h1>
        <StatusPill status={set.status} />
      </div>
      <p className="text-[13px] text-ink-2">
        {name} · Service: {serviceLabel} · {set.members.length} member
        {set.members.length === 1 ? "" : "s"} · Created {formatDateTime(set.createdAt)}
      </p>

      {/* Failed-set honesty: successful members stay up and quota-charged. */}
      {failed && live ? (
        <div className="mt-4 flex items-start gap-2 rounded-fluent border border-err bg-err-bg px-3 py-[10px] text-[13px] leading-[1.5]">
          <Mi
            name="warn"
            size={16}
            color="var(--color-err)"
            style={{ flexShrink: 0, marginTop: 1 }}
          />
          <div>
            <div className="font-semibold text-err-text">
              Provisioning failed — members left running
            </div>
            The members that came up are{" "}
            <strong>still running and still counting against your quota</strong>. They are not
            cleaned up automatically. Delete the set to remove them and free the quota.
          </div>
        </div>
      ) : null}

      {/* Set-level actions. Start/Stop/Delete are held while transitional. */}
      <div className="mt-4 flex items-center gap-2">
        <Button
          variant="secondary"
          disabled={transitional || action.isPending}
          onClick={() => action.mutate({ setId: set.id, action: "start" })}
        >
          Start
        </Button>
        <Button
          variant="secondary"
          disabled={transitional || action.isPending}
          onClick={() => action.mutate({ setId: set.id, action: "stop" })}
        >
          Stop
        </Button>
        <Button variant="danger" disabled={transitional} onClick={onDelete}>
          Delete
        </Button>
      </div>

      {/* Per-member table. */}
      <Card className="mt-5">
        <h3 className="px-4 pt-[14px] pb-[10px] text-[14px] font-semibold">Members</h3>
        <table className="w-full border-collapse text-[13px]">
          <thead>
            <tr>
              {["Member", "Role", "Status", "VMID", "Node", ""].map((h, i) => (
                <th
                  key={h || `sp-${i}`}
                  className="border-b border-line px-4 py-[6px] text-left font-semibold"
                >
                  {h}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {members.map((m) => {
              const gone = m.status === "tombstoned";
              return (
                <tr key={m.vmid} className="border-b border-line-row last:border-b-0">
                  <td className="flex h-10 items-center gap-2 px-4">
                    <Svc name="vm" size={16} />
                    {memberLabel(m)}
                  </td>
                  <td className="h-10 px-4 text-ink-2">{m.role || "—"}</td>
                  <td className="h-10 px-4">
                    <StatusPill status={m.status} label={MEMBER_STATUS_LABEL[m.status]} />
                  </td>
                  <td className="h-10 px-4 text-ink-2 tabular-nums">{m.vmid}</td>
                  <td className="h-10 px-4 text-ink-2">{m.node}</td>
                  <td className="h-10 px-4">
                    {gone ? (
                      <span className="text-ink-3">—</span>
                    ) : (
                      <Link
                        href={`/resources/${m.node}/${m.guestType}/${m.vmid}`}
                        className="text-[13px]"
                      >
                        Open blade
                      </Link>
                    )}
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </Card>

      {/* Next steps — surfaced when the control plane is reachable. No secret is
          ever rendered; the join token is not returned by the API. */}
      {ready && server?.connection ? (
        <Card className="mt-4 p-4">
          <h3 className="mb-2 text-[14px] font-semibold">Next steps</h3>
          <p className="mb-3 text-[13px] leading-[1.5] text-ink-2">
            The control plane is up. Fetch the cluster&apos;s kubeconfig from the server node over
            SSH — the join token was generated server-side and is never shown here.
          </p>
          <div className="mb-1 text-[12px] font-semibold text-ink-2">Control plane</div>
          <code className="block rounded-fluent border border-line bg-hover px-2 py-[7px] font-mono text-[13px] break-all select-all">
            {server.connection}
          </code>
          {svc.data?.docs ? (
            <p className="mt-3 text-[13px] text-ink-2">
              See the{" "}
              {/^https?:\/\//.test(svc.data.docs) ? (
                <a href={svc.data.docs} target="_blank" rel="noopener noreferrer">
                  service documentation
                </a>
              ) : (
                <span>{svc.data.docs}</span>
              )}{" "}
              for kubeconfig retrieval.
            </p>
          ) : null}
        </Card>
      ) : null}

      {confirmDelete ? (
        <DeleteSetFlyout
          set={set}
          name={name}
          live={live}
          onClose={onCloseDelete}
          onDeleted={onDeleted}
        />
      ) : null}
    </div>
  );
}

function DeleteSetFlyout({
  set,
  name,
  live,
  onClose,
  onDeleted,
}: {
  set: DeploymentSet;
  name: string;
  live: boolean;
  onClose: () => void;
  onDeleted: () => void;
}) {
  const [text, setText] = useState("");
  const del = useDeleteSet();
  const action = useSetAction();
  const match = text === name;

  return (
    <Flyout title="Delete deployment set" onClose={onClose}>
      <div className="mb-4 flex gap-[10px] rounded-fluent border border-err bg-err-bg px-3 py-[10px] text-[13px] leading-[1.5]">
        <Mi
          name="warn"
          size={16}
          color="var(--color-err)"
          style={{ flexShrink: 0, marginTop: 2 }}
        />
        <span>
          Deleting <strong>{name}</strong> destroys every member of the cluster. This is permanent
          and cannot be undone.
        </span>
      </div>

      {live ? (
        <div className="mb-4 rounded-fluent border border-line bg-hover px-3 py-[10px] text-[13px] leading-[1.5]">
          <p className="mb-2 text-ink-2">
            Members may still be running. Deleting purges the guests directly and expects them
            stopped — stop the cluster first for a clean teardown.
          </p>
          <Button
            variant="secondary"
            disabled={action.isPending}
            onClick={() => action.mutate({ setId: set.id, action: "stop" })}
          >
            Stop cluster first
          </Button>
        </div>
      ) : null}

      <div className="mb-[6px] text-[13px] font-semibold">This will also delete</div>
      <ul className="mb-[18px] ml-[18px] list-disc text-[13px] leading-[1.7] text-ink-2">
        <li>Every member guest ({set.members.length}) and its disks</li>
        <li>The members&apos; ownership records and reserved quota</li>
        <li>The cluster&apos;s rendered cloud-init snippets</li>
      </ul>

      <div className="mb-[6px] text-[13px]">
        Type <strong>{name}</strong> to confirm
      </div>
      <Input
        value={text}
        onChange={(e) => setText(e.target.value)}
        placeholder={name}
        aria-label="Confirm set name"
        className="w-full"
      />

      <div className="mt-4 flex gap-2">
        <Button
          variant="danger"
          disabled={!match || del.isPending}
          onClick={() =>
            del.mutate(set.id, {
              onSuccess: () => {
                onClose();
                onDeleted();
              },
            })
          }
        >
          Delete
        </Button>
        <Button variant="secondary" onClick={onClose}>
          Cancel
        </Button>
      </div>
    </Flyout>
  );
}
