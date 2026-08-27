"use client";
// Networking blade — design §3.5.5: NIC config card, live interfaces, and
// the real guest firewall (rules table + enable toggle).
import { CardError, Skeleton } from "@/components/dashboard/DashboardCards";
import {
  BladeHeading,
  BladeTable,
  bladeCell,
  bladeCellMuted,
  useGuestParams,
} from "@/components/guest/common";
import { Card } from "@/components/ui/Card";
import { StatusDot } from "@/components/ui/StatusDot";
import { Toggle } from "@/components/ui/Toggle";
import {
  useGuest,
  useGuestFirewall,
  useGuestInterfaces,
  useSetFirewall,
} from "@/lib/api/guestQueries";

function KV({ k, v }: { k: string; v: React.ReactNode }) {
  return (
    <div className="flex py-[3px] text-[13px]">
      <span className="w-[160px] flex-none text-ink-2">{k}</span>
      <span className="min-w-0 break-all">{v}</span>
    </div>
  );
}

export default function GuestNetworkingPage() {
  const g = useGuestParams();
  const guest = useGuest(g);
  const interfaces = useGuestInterfaces(g, guest.data?.status === "running");
  const firewall = useGuestFirewall(g);
  const setFw = useSetFirewall(g);

  if (guest.isPending) return <Skeleton className="h-40" />;
  if (guest.isError) return <CardError err={guest.error} />;
  const d = guest.data;

  return (
    <div>
      <BladeHeading>Network interfaces</BladeHeading>
      <Card className="mb-[18px] max-w-[560px] px-[14px] py-3">
        {d.nics.length === 0 ? (
          <p className="text-[13px] text-ink-2">No network interfaces configured.</p>
        ) : (
          d.nics.map((nic) => (
            <div
              key={nic.key}
              className="border-b border-line-row py-2 first:pt-0 last:border-b-0 last:pb-0"
            >
              <KV k="Interface" v={`${nic.key} (${nic.model || "—"})`} />
              <KV k="Bridge" v={nic.bridge || "—"} />
              <KV k="MAC address" v={nic.mac || "—"} />
              {nic.vlanTag ? <KV k="VLAN tag" v={String(nic.vlanTag)} /> : null}
              {nic.ipConfig ? <KV k="IP config" v={nic.ipConfig} /> : null}
              <KV k="Firewall" v={nic.firewall ? "Enabled on NIC" : "Disabled on NIC"} />
            </div>
          ))
        )}
      </Card>

      <BladeHeading>Live addresses</BladeHeading>
      <Card className="mb-[18px] max-w-[560px] px-[14px] py-3">
        {d.status !== "running" ? (
          <p className="text-[13px] text-ink-2">Guest is stopped — no live addresses.</p>
        ) : interfaces.isPending ? (
          <Skeleton className="h-12" />
        ) : interfaces.isError ? (
          <CardError err={interfaces.error} />
        ) : interfaces.data.agentUnavailable ? (
          <p className="text-[13px] text-ink-2">
            The QEMU guest agent is not running — install <code>qemu-guest-agent</code> inside the
            VM to see its IP addresses here.
          </p>
        ) : (
          interfaces.data.nics
            .filter((n) => n.name !== "lo")
            .map((n) => (
              <KV key={n.name} k={n.name} v={[...n.ipv4, ...n.ipv6].join(", ") || "no addresses"} />
            ))
        )}
      </Card>

      <div className="mb-2 flex items-center justify-between">
        <BladeHeading>Firewall</BladeHeading>
        {firewall.data ? (
          <div className="flex items-center gap-2 text-[13px]">
            <Toggle
              checked={firewall.data.enabled}
              onChange={(on) => setFw.mutate(on)}
              disabled={setFw.isPending}
              aria-label="Guest firewall"
            />
            <span className="text-ink-2">
              {firewall.data.enabled ? "Enabled" : "Disabled — rules below are inactive"}
            </span>
          </div>
        ) : null}
      </div>
      {firewall.isPending ? (
        <Skeleton className="h-20" />
      ) : firewall.isError ? (
        <CardError err={firewall.error} />
      ) : (firewall.data.rules ?? []).length === 0 ? (
        <p className="text-[13px] text-ink-2">
          No guest firewall rules defined. Rules can be managed in the Proxmox UI; Proxcloud shows
          and toggles them.
        </p>
      ) : (
        <BladeTable
          headers={[
            "Pos",
            "Type",
            "Action",
            "Source",
            "Dest",
            "Proto",
            "Port",
            "Enabled",
            "Comment",
          ]}
        >
          {firewall.data.rules.map((r) => (
            <tr key={r.pos} className="border-b border-line-row last:border-b-0">
              <td className={`${bladeCellMuted} tabular-nums`}>{r.pos}</td>
              <td className={bladeCellMuted}>{r.type}</td>
              <td className={bladeCell}>
                <span
                  style={{
                    color:
                      r.action === "ACCEPT"
                        ? "var(--color-ok)"
                        : r.action === "DROP" || r.action === "REJECT"
                          ? "var(--color-err)"
                          : undefined,
                  }}
                >
                  {r.action}
                </span>
              </td>
              <td className={bladeCellMuted}>{r.source || "Any"}</td>
              <td className={bladeCellMuted}>{r.dest || "Any"}</td>
              <td className={bladeCellMuted}>{r.proto || "any"}</td>
              <td className={`${bladeCellMuted} tabular-nums`}>{r.dport || "any"}</td>
              <td className={bladeCell}>
                <StatusDot
                  status={r.enable ? "active" : "stopped"}
                  label={r.enable ? "on" : "off"}
                />
              </td>
              <td className={bladeCellMuted}>{r.comment}</td>
            </tr>
          ))}
        </BladeTable>
      )}
    </div>
  );
}
