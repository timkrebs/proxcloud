"use client";
// Access control blade — design §3.5.3: real Proxmox ACL entries whose
// path covers this guest (read-only in v1).
import { CardError, Skeleton } from "@/components/dashboard/DashboardCards";
import {
  BladeHeading,
  BladeTable,
  bladeCell,
  bladeCellMuted,
  useGuestParams,
} from "@/components/guest/common";
import { useGuestACL } from "@/lib/api/guestQueries";

export default function GuestAccessPage() {
  const g = useGuestParams();
  const acl = useGuestACL(g);

  return (
    <div>
      <BladeHeading>Access control (IAM)</BladeHeading>
      <p className="mb-3 -mt-1 text-[12px] text-ink-2">
        Proxmox role assignments that apply to this guest. Inherited entries come from parent paths.
      </p>
      {acl.isPending ? (
        <Skeleton className="h-24" />
      ) : acl.isError ? (
        <CardError err={acl.error} />
      ) : (acl.data ?? []).length === 0 ? (
        <p className="text-[13px] text-ink-2">No role assignments visible to this token.</p>
      ) : (
        <BladeTable headers={["Name", "Type", "Role", "Scope"]}>
          {(acl.data ?? []).map((e, i) => (
            <tr key={i} className="border-b border-line-row last:border-b-0">
              <td className={bladeCell}>{e.ugid}</td>
              <td className={bladeCellMuted}>{e.type}</td>
              <td className={bladeCell}>{e.role}</td>
              <td className={bladeCellMuted}>
                {e.path === `/vms/${g.vmid}` ? "This guest" : `${e.path} (inherited)`}
              </td>
            </tr>
          ))}
        </BladeTable>
      )}
    </div>
  );
}
