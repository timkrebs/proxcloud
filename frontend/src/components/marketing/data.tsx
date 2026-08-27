// Marketing content — the design source's data arrays (navLinks, services,
// serviceCards, featureRows, steps, proofs, apiPoints, CODE, priceCards,
// footerCols), verbatim copy. Icons are marketing SVG components; every href is
// mapped to a REAL destination per ADR-0021 (no dead "#" links): in-page anchors
// (/#services, /#features, /#how, /#api, /#pricing, /#top), the stub marketing
// routes (/products, /solutions, /pricing, /support, /docs), the portal
// (/signin), or the one GitHub URL.
import type { ReactNode } from "react";

import { LineIcon, MarketingSvc } from "./icons";
import { CatalogMock, GovMock, WizardMock } from "./mocks";

export const GITHUB_URL = "https://github.com/timkrebs/proxcloud";

const PERSON =
  "M8 7.5a2.8 2.8 0 1 0 0-5.6 2.8 2.8 0 0 0 0 5.6zM2.5 14c.8-2.6 2.9-4 5.5-4s4.7 1.4 5.5 4";
const SERVER = "M2 3.5h12v6H2zM4.5 12.5h7M8 9.5v3";
const RING = "M8 14.5A6.5 6.5 0 1 0 8 1.5a6.5 6.5 0 0 0 0 13zM5.5 5.5l5 5";
const BOLT = "M9 1.5 3 9h4l-1 5.5L12 7H8z";

export interface NavLink {
  label: string;
  href: string;
}

/** Desktop nav (right of the Products mega-menu button). */
export const navLinks: NavLink[] = [
  { label: "Solutions", href: "/solutions" },
  { label: "Pricing", href: "/pricing" },
  { label: "Docs", href: "/docs" },
  { label: "Support", href: "/support" },
];

/** Mobile hamburger links — Products included as a real item. */
export const mobileLinks: NavLink[] = [
  { label: "Products", href: "/#services" },
  { label: "Solutions", href: "/solutions" },
  { label: "Pricing", href: "/pricing" },
  { label: "Docs", href: "/docs" },
  { label: "Support", href: "/support" },
];

export interface MegaService {
  name: string;
  short: string;
  icon: ReactNode;
}

/** Products mega-menu — items scroll to the landing #services section. */
export const megaServices: MegaService[] = [
  { name: "Virtual Machines", short: "Linux and Windows VMs from templates.", icon: <MarketingSvc name="vm" size={26} /> },
  { name: "Kubernetes", short: "Managed K3s with node pools and upgrades.", icon: <MarketingSvc name="k8s" size={26} /> },
  { name: "Databases", short: "PostgreSQL, MongoDB, and Redis.", icon: <MarketingSvc name="db" size={26} /> },
  { name: "Networking", short: "Isolated networks, firewalls, load balancers.", icon: <MarketingSvc name="net" size={26} /> },
  { name: "Storage", short: "Block volumes and S3-compatible buckets.", icon: <MarketingSvc name="store" size={26} /> },
  { name: "Service Catalog", short: "One place to create anything you offer.", icon: <MarketingSvc name="catalog" size={26} /> },
];

export interface Proof {
  title: string;
  desc: string;
  icon: ReactNode;
}

export const proofs: Proof[] = [
  { title: "Multi-tenant by design", desc: "Tenants are a hard isolation boundary, not a naming convention.", icon: <LineIcon d={PERSON} size={22} /> },
  { title: "Runs on your Proxmox", desc: "No new hypervisor to learn. Point it at the cluster you already operate.", icon: <LineIcon d={SERVER} size={22} /> },
  { title: "No per-core licensing", desc: "Self-hosted software. Capacity is limited by your hardware, not a contract.", icon: <LineIcon d={RING} size={22} /> },
  { title: "API-first and Terraform-ready", desc: "The portal is one client of a public API. Your pipelines are another.", icon: <LineIcon d={BOLT} size={22} /> },
];

export interface ServiceCard {
  name: string;
  desc: string;
  cta: string;
  href: string;
  icon: ReactNode;
  soon?: boolean;
}

export const serviceCards: ServiceCard[] = [
  { name: "Virtual Machines", desc: "Ubuntu, Debian, and Windows templates with T-shirt sizes, extra disks, cloud-init, snapshots, and a browser console.", cta: "Learn more", href: "/products", icon: <MarketingSvc name="vm" size={32} /> },
  { name: "Kubernetes", desc: "Managed K3s clusters with node pools you scale in place, in-place version upgrades, and one-click kubeconfig.", cta: "Learn more", href: "/products", icon: <MarketingSvc name="k8s" size={32} /> },
  { name: "Databases", desc: "PostgreSQL, MongoDB, and Redis with automated backups, retention policies, and connection strings on day one.", cta: "Learn more", href: "/products", icon: <MarketingSvc name="db" size={32} /> },
  { name: "Networking", desc: "Per-tenant virtual networks over Proxmox SDN, with subnets, firewall rules, NAT, public IPs, and simple load balancers.", cta: "Learn more", href: "/products", icon: <MarketingSvc name="net" size={32} /> },
  { name: "Storage", desc: "Block volumes that attach and detach from VMs, plus S3-compatible buckets with their own access keys and quotas.", cta: "Learn more", href: "/products", icon: <MarketingSvc name="store" size={32} /> },
  { name: "Service Catalog", desc: "Everything a tenant can create in one browsable grid — including the internal services your platform team publishes.", cta: "Learn more", href: "/products", icon: <MarketingSvc name="catalog" size={32} /> },
  { name: "Secrets", desc: "Vault-backed secrets, scoped per project and injectable into VMs and clusters at provisioning time.", cta: "On the roadmap", href: "/products", icon: <MarketingSvc name="secrets" size={32} />, soon: true },
  { name: "DNS", desc: "Tenant-scoped DNS zones with records managed alongside the resources they point at.", cta: "On the roadmap", href: "/products", icon: <MarketingSvc name="dns" size={32} />, soon: true },
];

export interface FeatureRow {
  kicker: string;
  title: string;
  body: string;
  bullets: string[];
  link: string;
  href: string;
  visual: ReactNode;
  /** When true, the text column sits after the visual on wide screens. */
  reversed: boolean;
}

export const featureRows: FeatureRow[] = [
  {
    kicker: "Self-service provisioning",
    title: "Minutes to a running VM, without a ticket",
    body: "A guided create flow with the tabs your users already expect: basics, size, disks, networking, tags, review. Validation happens before anything is provisioned, and a live estimate shows the monthly cost as they choose.",
    bullets: [
      "Templates and T-shirt sizes instead of raw hypervisor settings",
      "Deployment progress per resource — nothing blocks the user",
      "Every create flow ends in a resource page, not a dead end",
    ],
    link: "See the create flow",
    href: "/signin",
    visual: <WizardMock />,
    reversed: false,
  },
  {
    kicker: "Multi-tenancy and governance",
    title: "Isolation you can hand to another team",
    body: "Each tenant carries its own users, networks, quotas, activity log, and cost view. Owner, Contributor, and Reader assignments made at tenant scope flow down to every project inside it.",
    bullets: [
      "Hard boundaries: no cross-tenant visibility, ever",
      "vCPU, RAM, and storage quotas per tenant and per project",
      "Full audit trail — who did what, to which resource, when",
    ],
    link: "Explore access control",
    href: "/signin",
    visual: <GovMock />,
    reversed: true,
  },
  {
    kicker: "Managed-style services",
    title: "Databases and clusters from a catalog",
    body: "Your platform team publishes services once; tenants create them on demand. Backups, retention, maintenance windows, and health checks come configured rather than documented.",
    bullets: [
      "PostgreSQL, MongoDB, Redis, and managed K3s in v1",
      "Backups and maintenance windows set at creation time",
      "Publish internal services into the same catalog",
    ],
    link: "Browse the catalog",
    href: "/signin",
    visual: <CatalogMock />,
    reversed: false,
  },
];

export interface Step {
  n: string;
  title: string;
  desc: string;
  icon: ReactNode;
}

export const steps: Step[] = [
  { n: "1", title: "Connect your Proxmox", desc: "Add your cluster nodes and storage, import templates, and set the node capacity Proxcloud is allowed to use.", icon: <LineIcon d={SERVER} size={20} /> },
  { n: "2", title: "Invite your tenants", desc: "Create a tenant per team or customer, set its quotas, and assign Owner, Contributor, or Reader roles.", icon: <LineIcon d={PERSON} size={20} /> },
  { n: "3", title: "Users self-serve resources", desc: "They open the portal, pick a service, and provision inside their own project. You watch capacity, not tickets.", icon: <LineIcon d={BOLT} size={20} /> },
];

export const apiPoints: string[] = [
  "A pcctl CLI for day-to-day operations and scripting",
  "A Terraform provider for VMs, clusters, networks, and databases",
  "REST with async operation handles — poll or subscribe, never block",
];

export interface CodeSnippet {
  label: string;
  body: string;
}

export const CODE: CodeSnippet[] = [
  {
    label: "pcctl",
    body: `# authenticate against your tenant
$ pcctl login --tenant aurora-labs

# create a virtual machine from a template
$ pcctl vm create web-prod-02 \\
    --project web-prod \\
    --image ubuntu-24.04 \\
    --size M \\
    --subnet default \\
    --public-ip \\
    --ssh-key alex-workstation

deployment  deploy-web-prod-02   started
vm          web-prod-02          Provisioning
nic         web-prod-02-nic      Created
ip          203.0.113.42         Created

# watch it settle
$ pcctl vm get web-prod-02 --watch
web-prod-02  Running  10.10.1.12  4 vCPU / 8 GB`,
  },
  {
    label: "Terraform",
    body: `terraform {
  required_providers {
    proxcloud = {
      source  = "proxcloud/proxcloud"
      version = "~> 0.4"
    }
  }
}

provider "proxcloud" {
  endpoint = "https://proxcloud.example/api/v1"
  tenant   = "aurora-labs"
}

resource "proxcloud_kubernetes_cluster" "apps" {
  name    = "apps-prod"
  project = "web-prod"
  version = "v1.31"

  node_pool {
    size  = "M"
    count = 3
  }

  tags = {
    env = "prod"
  }
}`,
  },
  {
    label: "REST",
    body: `$ curl -X POST \\
    https://proxcloud.example/api/v1/vms \\
    -H "Authorization: Bearer $PC_TOKEN" \\
    -H "Content-Type: application/json" \\
    -d '{
      "name": "web-prod-02",
      "project": "web-prod",
      "image": "ubuntu-24.04",
      "size": "M",
      "network": {
        "subnet": "default",
        "public_ip": true,
        "allow_inbound": ["ssh", "https"]
      }
    }'

HTTP/1.1 202 Accepted
Location: /api/v1/operations/op_8f21c4
{
  "id": "vm_7c11e9",
  "status": "Provisioning"
}`,
  },
];

export interface PriceCard {
  kicker: string;
  title: string;
  desc: string;
  items: string[];
  featured: boolean;
}

export const priceCards: PriceCard[] = [
  {
    kicker: "The platform",
    title: "Self-hosted, no per-core fee",
    desc: "Proxcloud runs next to your Proxmox cluster. Capacity is bounded by the hardware you already bought.",
    items: [
      "Unlimited tenants, projects, and users",
      "No metered egress or API call charges",
      "Upgrade on your own schedule",
    ],
    featured: true,
  },
  {
    kicker: "Internal showback",
    title: "Flat prices you set",
    desc: "Give every resource type a flat monthly price so teams see what they consume — for chargeback, or just for awareness.",
    items: [
      "Live cost estimate in every create flow",
      "Cost per tenant, project, and tag",
      "Export monthly usage for finance",
    ],
    featured: false,
  },
];

export interface FooterCol {
  title: string;
  links: NavLink[];
}

export const footerCols: FooterCol[] = [
  {
    title: "Products",
    links: [
      { label: "Virtual Machines", href: "/#services" },
      { label: "Kubernetes", href: "/#services" },
      { label: "Databases", href: "/#services" },
      { label: "Networking", href: "/#services" },
      { label: "Storage", href: "/#services" },
    ],
  },
  {
    title: "Solutions",
    links: [
      { label: "Platform teams", href: "/solutions" },
      { label: "Managed service providers", href: "/solutions" },
      { label: "Research and lab clusters", href: "/solutions" },
      { label: "Migration from public cloud", href: "/solutions" },
    ],
  },
  {
    title: "Resources",
    links: [
      { label: "Docs", href: "/docs" },
      { label: "API reference", href: "/docs" },
      { label: "Terraform provider", href: "/docs" },
      { label: "GitHub", href: GITHUB_URL },
    ],
  },
  {
    title: "Company",
    links: [
      { label: "About", href: "/#top" },
      { label: "Pricing", href: "/pricing" },
      { label: "Contact", href: "/support" },
      { label: "Go to portal", href: "/signin" },
    ],
  },
];
