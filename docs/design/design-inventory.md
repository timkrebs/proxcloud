I've read the complete decoded file (1,556 lines). Here is the exhaustive design inventory.

---

# Proxcloud — Complete Design Inventory

Source: `Proxcloud.dc.html` (single-file x-dc template, ~1,556 lines decoded). Everything is inline `style="…"` — there is **no** class-based CSS beyond a small `<helmet><style>` reset. Hover/focus/active states use custom attributes `style-hover`, `style-focus`, `style-active` on elements.

---

## 1. DESIGN TOKENS

### 1.1 Global reset / base (the only stylesheet block)

```css
*{box-sizing:border-box;margin:0;padding:0}
html,body{height:100%}
body{font-family:"Segoe UI",system-ui,-apple-system,"Inter",sans-serif;color:#323130;background:#FAF9F8;font-size:14px}
a{color:#0078D4;text-decoration:none}
a:hover{color:#005A9E;text-decoration:underline}
button,input,select,textarea{font-family:inherit}
input::placeholder,textarea::placeholder{color:#A19F9D}
```

### 1.2 Color palette (Microsoft Fluent / Azure Portal neutrals + communication blue)

**Surfaces & backgrounds**
| Hex | Role |
|---|---|
| `#FAF9F8` | App canvas / main content background; sign-in & sign-up page bg; landing "services" strip bg |
| `#FFFFFF` | Cards, left nav, resource menu, tables, panes, wizard footer bar, inputs, landing hero |
| `#F3F2F1` | Universal hover background (nav items, table cmd buttons, secondary button hover); table `<thead>` background on list pages; tag/chip background; progress-bar track; `<pre>` JSON background; disabled Delete button background |
| `#EFF6FC` | Selected-row background, selected size-card background, bulk-selection bar background, active tenant/project row background, landing eyebrow badge background |
| `#DEECF9` | **Active nav item background**; sparkline area fill; spinner track ring; bulk-bar button hover; landing eyebrow badge border; palette hint chip border context |
| `#1B1A19` | Dark top bar background (40px app bar) and landing header (56px) |
| `#3B3A39` | Top-bar button hover; center search-box background/border |
| `#484644` | Center search-box hover background |

**Text hierarchy**
| Hex | Role |
|---|---|
| `#323130` | Primary text (body default) |
| `#605E5C` | Secondary text: subtitles, table secondary cells, help copy, labels, breadcrumb separators/current crumb, muted icons |
| `#A19F9D` | Tertiary/disabled: placeholders, disabled command-bar labels, locked wizard tabs, notification timestamps, palette hints, "Previous" disabled |
| `#FFFFFF` | Text on dark bar / primary buttons |
| `#C8C6C4` | Top-bar search placeholder text; tenant sub-label in user chip; landing "Sign in" hover |

**Borders & dividers**
| Hex | Role |
|---|---|
| `#EDEBE9` | Standard border/divider: card borders, nav dividers, table header bottom border, tab strip underline, section rules, pane footers |
| `#F3F2F1` | Table **row** separators (lighter than card border), catalog card footer rule |
| `#8A8886` | Input/select/textarea border; secondary button border; unchecked checkbox/radio border |
| `#C8C6C4` | Unselected size-card border; dashed filter-pill border; scrollbar thumb; large empty-state icon stroke |
| `#0078D4` | Focus border (`style-focus="border-color:#0078D4"`), selected states |
| `#D13438` | Invalid input border |

**Accent / brand**
| Hex | Role |
|---|---|
| `#0078D4` | Primary accent: links, primary button, active tab underline, progress fill, avatar circle, chart line, notification badge, selected radio/checkbox fill |
| `#106EBE` | Primary button hover |
| `#005A9E` | Primary button active; link hover |
| `#005BA1` | Logo dark facet; product-icon dark facet |
| `#50E6FF` | Logo light cyan facet; product-icon light facet |
| `#C3F1FF` | Product-icon tertiary (k8s spoke strokes, LB inner dot) |

**Status colors** (from `statusColor(st)`)
| Hex | Applies to |
|---|---|
| `#107C10` (green) | `Running`, `Healthy`, `Available`, `Active`, `Succeeded`, `Created`, `Attached`; firewall `Allow`; service-health dots; validation-passed banner |
| `#605E5C` (grey) | `Stopped`, `Pending` |
| `#D13438` (red) | `Failed`, `Deny`; danger button; bulk Delete label; trash-icon hover |
| `#0078D4` (blue) | Everything else — `Provisioning`, `Creating`, `Starting`, `Stopping`, `Restarting`, `Resizing`, `In progress` |
| `#DFF6DD` | Success banner background (validation passed) |
| `#FDE7E9` | Error banner background (validation failed, delete warning) |
| `#A4262C` | Error text: required asterisk `*`, inline field errors, auth errors, error-banner heading |
| `#FFB900` | **Operator mode**: badge background + 3px strip under top bar |

**Product-icon fills** (`svc()`)
- VM: `#005BA1` chassis, `#50E6FF` screen, `#0078D4` diagonal, `#8A8886` stand
- K8s: `#0078D4` hex, `#fff` hub, `#50E6FF` nodes, `#C3F1FF` spokes
- PostgreSQL: `#0078D4` body, `#50E6FF` top ellipse
- MongoDB: `#107C10` body, `#9FD89F` top ellipse
- Redis: `#D13438` body, `#F1BBBC` top ellipse
- Network: `#0078D4` / `#005BA1` nodes, `#50E6FF` links
- Load balancer: `#0078D4` disc, `#C3F1FF` core, `#50E6FF` arms
- Block volume: three stacked bars `#005BA1` / `#0078D4` / `#50E6FF`
- S3 bucket: `#0078D4` body, `#50E6FF` rim
- All resources: 4 squares `#0078D4`, `#50E6FF`, `#50E6FF`, `#005BA1`

**Chart colors**: line `#0078D4` (1.2px, `vectorEffect:non-scaling-stroke`), area `#DEECF9` at `opacity:.7`, gridlines `#F3F2F1` (0.6px).

### 1.3 Typography
- **Sans**: `"Segoe UI", system-ui, -apple-system, "Inter", sans-serif`
- **Mono**: `'Cascadia Code', Consolas, monospace` — used only in the cloud-init textarea (12.5px) and the JSON pane `<pre>` (12px)
- `font-variant-numeric:tabular-nums` on: costs, quota values, timestamps, prices, disk sizes, firewall priorities/ports, metric values

**Font-size scale and where used**
| Size | Usage |
|---|---|
| 10px | Operator badge; notification count badge; tenant name in user chip (uppercase, ls .3px) |
| 11px | Nav "Favorites" label; resource-menu group headers (600, uppercase, ls .3px); tag chips in resource table; notification timestamps; palette hints & Esc chip |
| 12px | Breadcrumbs; secondary/help text; card meta; catalog card descriptions; service-health labels; size-card prices; status pill; filter pills; footer text; sign-up hint text |
| 12.5px | Cloud-init textarea; landing service descriptions |
| 13px | Table body text; nav item labels; links inside pages; button text (small); form help; essentials rows; toast title; pane body text |
| 14px | Base body; card titles (600); form field labels; buttons; wizard tab labels; inputs/selects; SSO button |
| 15px | Top-bar wordmark (600, ls .2px); landing feature titles; auth email/password inputs |
| 16px | Wizard section headings (600); VM blade section headings (600); size-card letter (600); landing/auth wordmark |
| 17px | Landing hero subhead |
| 18px | Side-pane titles (600); placeholder screen title |
| 22px | Deployment screen title |
| 24px | **Page H1** (Dashboard greeting, Create a resource, All resources, Activity log, VM name, Sign in, Request a tenant) |
| 26px | Cost-this-month figure |
| 44px | Landing hero H1 (600, line-height 1.15, ls -.5px, `text-wrap:balance`) |

`text-wrap:pretty` used on landing paragraphs/descriptions.

### 1.4 Border radii
- **2px** — the dominant radius (Azure/Fluent): every card, button, input, select, badge, chip, banner, pane, table container, modal, toast, pre
- **13px** — filter pills (height 26 → full pill)
- **10px** — toggle track (40×20)
- **50%** — status dots, avatar, radio circles, spinner ring, knob
- **5px** — scrollbar thumb
- **0.5–1px** — inside product-icon SVG rects

### 1.5 Shadows
| Token | Value | Used on |
|---|---|---|
| Card (depth-4) | `0 1.6px 3.6px rgba(0,0,0,.132), 0 .3px .9px rgba(0,0,0,.108)` | Dashboard cards, service tiles, catalog cards, cost card, tables, deployment card, chart cards |
| Card hover (depth-8) | `0 3.2px 7.2px rgba(0,0,0,.132), 0 .6px 1.8px rgba(0,0,0,.108)` | Service tile hover, catalog card hover |
| Auth card | `0 2px 6px rgba(0,0,0,.13)` | Sign-in card, sign-up card, SSO button |
| Side pane | `-6px 0 18px rgba(0,0,0,.18)` | All 4 right-side panes |
| Toast | `0 6px 16px rgba(0,0,0,.18)` | Toasts |
| Command palette | `0 12px 40px rgba(0,0,0,.3)` | Palette dialog |
| Selection ring | `inset 0 0 0 1px #0078D4` | Selected size card (doubles the 1px border) |

Overlay scrim: `rgba(0,0,0,.25)` (command palette only).

### 1.6 Spacing patterns
- Page padding: `20px 32px 40px` (most), `24px 32px 40px` (dashboard), `20px 32px 0` (wizard, footer supplies bottom), `80px 32px` (placeholder)
- Page max-widths: `1360px` (dashboard, catalog, resources, activity), `1200px` (wizard), `900px` (deployment)
- Card padding: `14px 16px` (dashboard widgets), `16px` (catalog/cost cards), `12px 14px` (chart tiles), `40px` (auth cards, empty state)
- Table cell padding: `0 12px` / `0 16px` / `0 8px`; row height `40px` (`38px` for firewall rows); header padding `8px 12px` (list pages) or `6px 16px` (embedded dashboard table)
- Form row: label `flex:0 0 220px`, control `width:300px`, `margin-bottom:14px`
- Section rule: `<div style="height:1px;background:#EDEBE9;margin:8px 0 14px">`
- Control heights: 32px (standard button/input/select), 28px (compact search / bulk buttons), 34px (auth underline inputs), 36px (nav item, command-bar button), 38px (landing CTA), 44px (SSO button, palette input)

### 1.7 Scrollbar
```css
::-webkit-scrollbar{width:10px;height:10px}
::-webkit-scrollbar-thumb{background:#C8C6C4;border-radius:5px}
::-webkit-scrollbar-track{background:transparent}
```

### 1.8 Animations (keyframes)
```css
@keyframes pcspin  {to{transform:rotate(360deg)}}                                  /* spinner: 1s linear infinite */
@keyframes pcslide {from{transform:translateX(60px);opacity:.4} to{transform:translateX(0);opacity:1}}  /* panes: .18s ease */
@keyframes pctoast {from{opacity:0;transform:translateY(-6px)} to{opacity:1;transform:translateY(0)}}   /* toasts: .2s ease */
```
Transitions: nav width `.15s ease`; toggle knob `left .15s` + `background .15s`; chevron `transform .15s`; notification progress bar `width .4s`.

---

## 2. GLOBAL LAYOUT

### 2.1 Root
```
<div style="display:flex;flex-direction:column;height:100vh;overflow:hidden">   [Proxcloud portal]
```
`isApp` = route not in `landing|signin|signup`. Landing/signin/signup replace the whole chrome.

### 2.2 Top bar — height **40px**, `background:#1B1A19`, `z-index:40`
Left → right:
1. **Hamburger** — 44×40 button, white 16px 3-line icon (`M2 4.5h12M2 8h12M2 11.5h12`, stroke-width 1.4), `title="Toggle navigation"`, hover `#3B3A39`. Toggles nav 220 ⇄ 48px.
2. **Logo + wordmark** — 18px hexagon logo (3 paths: `#0078D4` body, `#50E6FF` top facet, `#005BA1` left facet), then `Proxcloud` at 15px/600, ls .2px, white. Whole thing is a button → home.
3. **Operator badge** (conditional, `isOperator`) — `Operator` uppercase, 10px/600, ls .4px, `color:#1B1A19`, `background:#FFB900`, radius 2, padding 2px 6px.
4. **Center search** — absolutely positioned `left:50%;transform:translateX(-50%)`, `width:min(44vw,560px)`; button 28px tall, bg/border `#3B3A39`, radius 2, `color:#C8C6C4`, 13px, magnifier icon + text **"Search resources, services, and docs (Cmd+K)"**; hover `#484644`. Opens command palette.
5. **Right cluster** (`margin-left:auto`, stretch to 40px):
   - **Bell** (`title="Notifications"`) 44px — outline bell icon; badge when `unread>0`: absolute `top:5px;right:7px`, min-width 14, height 14, radius 7, bg `#0078D4`, white 10px/600, shows count.
   - **Gear** (`title="Settings"`) 44px — 8-spoke gear.
   - **Question circle** (`title="Help + support"`) 44px.
   - **User chip** (`title="Switch tenant or project"`) padding 0 12, gap 8: right-aligned stack of `Alex Meyer` (12px/600 white) over tenant name (`aurora-labs`, 10px, `#C8C6C4`, uppercase, ls .3px), then 26px circular avatar `#0078D4` with white 11px/600 initials `AM`.
   All hover `#3B3A39`.

**Operator strip**: when `isOperator`, a `height:3px;background:#FFB900` bar sits directly under the top bar.

### 2.3 Horizontal scroll frame
```
<div style="display:flex;flex:1;overflow-x:auto;overflow-y:hidden">
  <div style="min-width:1280px;flex:1;display:flex;overflow:hidden">
```
The app never reflows below 1280px — it scrolls horizontally instead.

### 2.4 Left nav — width `{{navW}}` (**220px** expanded / **48px** collapsed)
`background:#fff; border-right:1px solid #EDEBE9; overflow-x:hidden; overflow-y:auto; transition:width .15s ease; padding:8px 0`

Every item: full-width button, **height 36px**, font 13px, `color:#323130`, `white-space:nowrap`, icon slot `width:48px;flex:0 0 48px` centered, label truncates with ellipsis. Hover `#F3F2F1`. Active background `#DEECF9` (via `navBg(on)`), otherwise `transparent`. Collapsing to 48px simply clips labels (icon slot is exactly 48px, so icons stay centered); `title` attributes provide tooltips.

Order:
1. **Create a resource** — blue plus icon (`#0078D4`, sw 1.5) → catalog
2. divider (`height:1px;background:#EDEBE9;margin:6px 12px`)
3. `navMain`: **Home** (home/house outline, `#0078D4`, sw 1.4) · **All resources** (4-square multicolor `allres` icon, 16px)
4. divider
5. Section label **"Favorites"** — `padding:2px 0 4px 14px;font-size:11px;color:#605E5C`
6. `navFavs` (all 17px product icons): **Virtual machines** (vm) · **Kubernetes** (k8s) · **Databases** (pg) · **Networking** (net) · **Storage** (vol)
7. divider
8. `navBottom` (16px line icons, `#605E5C`): **Access control (IAM)** (person) · **Activity log** (clock) · **Settings** (gear)

Active detection: `route==='resources' && resType===<type>`.

### 2.5 Main content region
`flex:1;overflow-y:auto;background:#FAF9F8;position:relative` — hosts all `sc-if` routes.

### 2.6 Breadcrumbs
Inline text, 12px, `margin-bottom:10px`. Pattern:
`<a>Home</a>` + `<span style="color:#605E5C"> &gt; </span>` + … + current crumb in `#605E5C` (not a link).
- Catalog: `Home > Create a resource`
- Wizard: `Home > Create a resource > Create a virtual machine`
- Deployment: `Home > deploy-{name}`
- VM detail: `Home > Virtual machines > {vmName}`
- Resources: `Home > {resTitle}`
- Activity: `Home > Activity log`

### 2.7 Toasts
Container: `position:fixed;top:48px;right:12px;z-index:70;display:flex;flex-direction:column;gap:8px;pointer-events:none`.
Each toast: `width:340px`, bg `#fff`, `border:1px solid #EDEBE9`, **`border-left:3px solid {accent}`**, radius 2, shadow `0 6px 16px rgba(0,0,0,.18)`, padding `12px 14px`, `display:flex;gap:10px`, `animation:pctoast .2s ease`, `pointer-events:auto`.
- Icon: `ok`→ checkC `#107C10`; `info`→ info `#0078D4`; `err`→ warn `#D13438` (all 16px)
- Accent map: `{ok:'#107C10', info:'#0078D4', err:'#D13438'}`
- Title 13px/600, description 12px `#605E5C` line-height 1.4
- Auto-dismiss after **4200ms**

### 2.8 Modal / dialog patterns
There are **no centered modals** except the command palette. Everything else is an **Azure-style right-side pane** (blade):
`position:fixed;top:40px;right:0;bottom:0;width:400px` (JSON pane 440px), `background:#fff`, shadow `-6px 0 18px rgba(0,0,0,.18)`, `z-index:50`, `display:flex;flex-direction:column`, `animation:pcslide .18s ease`.
- Header: `padding:16px 20px 12px`, `display:flex;justify-content:space-between`, title 18px/600, close ✕ button (14px, `#605E5C`, hover `#323130`, padding 6)
- Body: `flex:1;overflow-y:auto;padding:0 20px 20px`
- Footer (when present): `border-top:1px solid #EDEBE9;padding:14px 20px`

Four panes: **Tenant + project**, **Notifications**, **Delete**, **JSON view**. Only one at a time (`state.pane`).

### 2.9 Keyboard
- `Cmd/Ctrl + K` → toggle command palette (and clear query)
- `Escape` → close palette **and** any open pane
- Enter in auth email → Next; Enter in password → Sign in

### 2.10 Prototype props (design-tool knobs)
| Prop | Editor | Values |
|---|---|---|
| `operatorMode` | boolean | default `false` — section "Chrome" |
| `startRoute` | enum | `home, landing, signin, catalog, wizard, resources, activity`; default `home` — section "Prototype" |
| `deploySpeed` | range | 300–2500, step 100, default **900** ms — section "Prototype" |

---

## 3. EVERY SCREEN

Routes: `home, catalog, wizard, deploy, vm, resources, activity, placeholder, landing, signin, signup`.

---

### 3.1 Dashboard (`isHome`) — label "Dashboard"
`padding:24px 32px 40px;max-width:1360px`

**Header**
- `{{greeting}}` at 24px/600 — computed: `"Good morning|Good afternoon|Good evening, Alex"` (< 12h / < 18h / else)
- Subline 13px `#605E5C`, `margin-top:4px`: `Tenant `**`aurora-labs`**` · All projects` (tenant name bolded `#323130`)

**"Proxcloud services" tile row** — heading 14px/600, `margin:26px 0 10px`; `display:flex;gap:8px;flex-wrap:wrap`
Each tile: **104×96px** button, `#fff`, border `#EDEBE9`, radius 2, card shadow, column-centered, `gap:10px`, 12px label (centered, line-height 1.3), hover → depth-8 shadow.
Tiles (icon 26px): **Virtual machine** (vm → opens wizard directly) · **Kubernetes cluster** (k8s → catalog filtered) · **PostgreSQL** (pg) · **Virtual network** (net) · **S3 bucket** (bucket) · **See the catalog** (blue 24px plus, sw 1.2).

**Two-column grid**: `grid-template-columns:minmax(0,1fr) 340px;gap:16px;margin-top:28px;align-items:start`

**Left — "Recent resources" card**
- Header row `padding:14px 16px 10px`: title 14px/600 + right link **"See all resources"** (13px)
- Table, 13px, `border-collapse:collapse`. Header cells: `font-weight:600;padding:6px 16px|12px;border-bottom:1px solid #EDEBE9` — **no grey header background here** (unlike list pages).
- Columns: **Name** | **Type** | **Project** | **Last viewed**
- Rows: `border-bottom:1px solid #F3F2F1`, cell height 40px. Name is a link with an 18px product icon (gap 8). Type/Project/Last viewed in `#605E5C`; Last viewed uses tabular-nums.
- Data: `web-prod-01 / Virtual machine / web-prod / 2 h ago`; `apps-prod / Kubernetes cluster / web-prod / 5 h ago`; `orders-db / PostgreSQL 16 / web-prod / Yesterday`; `pg-primary / Virtual machine / data-staging / 2 d ago`; `data-lake / S3 bucket / data-staging / 3 d ago` (filtered to still-existing resources).

**Right column (340px) — three stacked cards, `gap:16px`, each `padding:14px 16px`**

1. **"Usage — aurora-labs"** (title 14px/600, mb 12). Per quota: label row 13px with right value `{used} of {limit}` in `#605E5C` tabular-nums, then a **4px** bar: track `#F3F2F1` radius 2, fill `#0078D4` radius 2, `width:{pct}%`. Items: `vCPU 32 of 48 (67%)`, `RAM 96 GB of 128 GB (75%)`, `Storage 1.2 TB of 2 TB (60%)`.
2. **"Cost this month"** — figure **€412.38** at 26px/600 tabular-nums; caption `Estimate · flat price model` 12px `#605E5C`; right side a 120px-wide **sparkline** (height 44, seed 7).
3. **"Service health"** — title 14px/600 mb 10; wrap flex `gap:8px 16px`; each item 12px `#605E5C` with a hard-coded **8px green `#107C10` dot**: `Compute`, `Kubernetes`, `Databases`, `Networking`, `Storage`.

---

### 3.2 Create a resource / Catalog (`isCatalog`) — label "Create a resource"
`padding:20px 32px 40px;max-width:1360px`. Breadcrumb; H1 **"Create a resource"** 24px/600 mb 18.

Two columns `display:flex;gap:24px;align-items:flex-start`:

**Left rail `flex:0 0 180px`**
- Label **"CATEGORIES"** — 12px/600, `#605E5C`, uppercase, ls .3px, `margin:2px 0 6px 8px`
- Buttons full-width, left-aligned, `padding:7px 8px`, 13px, radius 2, active bg `#DEECF9`, hover `#F3F2F1`.
- Categories: **All, Compute, Kubernetes, Databases, Networking, Storage**

**Right pane `flex:1`**
- Search input: `width:min(100%,420px);height:32px;border:1px solid #8A8886;radius 2;padding:0 10px;font-size:14px`, placeholder **"Search the catalog"**, focus border `#0078D4`
- Card grid: `repeat(auto-fill,minmax(250px,1fr));gap:12px;margin-top:16px`
- Card: `#fff`, border `#EDEBE9`, radius 2, card shadow, `padding:16px`, column, `gap:10px`, `min-height:150px`, hover depth-8. Contents: header row (24px product icon + name 14px/600), description 12px `#605E5C` line-height 1.45 (`flex:1`), footer `border-top:1px solid #F3F2F1;padding-top:9px` with a **"Create"** link (13px).

Catalog items (name / category / description):
| Item | Category | Description |
|---|---|---|
| Virtual machine | Compute | "Linux or Windows VM from a template, with disks, NICs, cloud-init, and web console access." |
| Kubernetes cluster | Kubernetes | "Managed K3s cluster with node pools, version upgrades, and kubeconfig download." |
| PostgreSQL | Databases | "Managed PostgreSQL 16 with automated backups and configurable retention." |
| MongoDB | Databases | "Managed MongoDB replica set with scheduled backups and connection strings." |
| Redis | Databases | "Managed Redis 7 cache with optional persistence and maintenance windows." |
| Virtual network | Networking | "Isolated VXLAN network with subnets, DHCP ranges, and firewall rules." |
| Load balancer | Networking | "Simple L4 load balancer with health checks and public frontend." |
| Block volume | Storage | "Attachable block storage volume for virtual machines." |
| S3 bucket | Storage | "S3-compatible object storage bucket with access keys and quotas." |

Only **Virtual machine** opens the wizard; the rest fire an info toast: *"Same pattern, later iteration — The {name} create flow reuses the VM wizard frame — tabs, cost card, review, deployment."*

---

### 3.3 Create a virtual machine wizard (`isWizard`) — label "Create a virtual machine"
`padding:20px 32px 0;max-width:1200px`. Breadcrumb; H1 24px/600.

**Tab strip (step indicator)** — `display:flex;gap:2px;border-bottom:1px solid #EDEBE9;margin-top:16px`
Each tab: button, `padding:9px 12px`, 14px, `border-bottom:2px solid {bord}`, `margin-bottom:-1px`.
- Active: color `#323130`, `font-weight:600`, border `#0078D4`
- Visited/enabled: color `#605E5C`, weight 400, border `transparent`
- **Locked** (index > `maxTab`): color `#A19F9D`, `disabled`
Tabs (7): **Basics · Size · Disks · Networking · Advanced · Tags · Review + create**

**Body**: `display:flex;gap:28px;align-items:flex-start;padding-top:20px`; left `flex:1;max-width:720px`, right cost card `flex:0 0 300px;position:sticky;top:20px`.

Shared form-row anatomy: `display:flex;align-items:center;margin-bottom:14px`; label `flex:0 0 220px;font-size:14px` with optional 13px info-circle (`#605E5C`, native `title` tooltip) and red `*` (`#A4262C`); control `width:300px;height:32px;border:1px solid #8A8886;radius 2`.

**Step 0 — Basics**
- Intro paragraph 13px `#605E5C` lh 1.5 mb 18: *"Create a virtual machine from a template. Complete the Basics tab, then review each tab or go straight to Review + create."* + `Learn more` link
- Section **"Project details"** 16px/600 + caption 12px `#605E5C`: *"Select the project this virtual machine will live in. Every resource belongs to exactly one project."*
  - **Project*** (tooltip: "Projects group resources inside a tenant, like Azure resource groups.") — select, `padding:0 6px`; options `web-prod`, `data-staging`, `sandbox`
- Section **"Instance details"** 16px/600 `margin-top:22px` + 1px rule
  - **Virtual machine name*** (tooltip: "1–40 characters: lowercase letters, numbers, and hyphens. Must start with a letter.") — input placeholder `e.g. web-prod-02`, border turns `#D13438` on error; error text below: 12px `#A4262C`, `margin-top:4px;max-width:300px`. Label row uses `align-items:flex-start` + `height:32px` on the label.
  - **Image*** — select: `Ubuntu 24.04 LTS`, `Debian 12`, `Windows Server 2022`
- Section **"Administrator account"** + rule
  - **SSH public key** (tooltip: "Injected via cloud-init on first boot.") — select: `alex-workstation — ED25519`, `ci-deploy — RSA 4096`, `Generate new key pair`

**Step 1 — Size**
- Heading **"Size"** 16px/600; caption: *"T-shirt sizes map to vCPU and RAM presets on the underlying nodes. You can resize later with a restart."*
- Grid `repeat(auto-fill,minmax(160px,1fr));gap:10px`
- Card button `padding:14px`, radius 2, `text-align:left`; **selected**: bg `#EFF6FC`, border `#0078D4`, `box-shadow:inset 0 0 0 1px #0078D4`, plus a 16px `checkC` icon in `#0078D4` at top-right; **unselected**: bg `#fff`, border `#C8C6C4`, no ring, hover `border-color:#0078D4`
- Card body: letter 16px/600; `{cpu} vCPU · {ram} GB RAM` 13px; `€{price} / month` 12px `#605E5C` tabular
- Sizes: **S** 2 vCPU / 4 GB / €18.00 · **M** 4 / 8 / €36.00 · **L** 8 / 16 / €72.00 · **XL** 16 / 32 / €144.00

**Step 2 — Disks**
- Heading **"Disks"**; caption: *"The OS disk is created from the selected image. Data disks are block volumes billed at €0.08 per GB per month."*
- Table 13px, headers `padding:6px 8px;border-bottom:1px solid #EDEBE9` (no bg): **Name | Type | Size | (blank action col)**
- Rows: height 40px, `border-bottom:1px solid #F3F2F1`; Type in `#605E5C`; Size shows `{n} GB` tabular; delete trash button (14px, `#605E5C`, hover `#D13438`, `title="Remove disk"`) only on data disks
- Row 1 is always `{name||'vm'}-osdisk` / `OS disk · from image` / `64 GB` / not deletable
- Add link-button below (`margin-top:12px`, `#0078D4`, 13px, plus icon, hover `#005A9E`): **"Add a data disk (128 GB)"** → appends `{name}-data-{n}` / `Data disk` / 128

**Step 3 — Networking**
- Heading **"Network interface"**; caption: *"The VM gets one NIC in the selected subnet. Firewall rules apply per network."*
- **Virtual network*** select: `vnet-web-prod (10.10.0.0/16)`, `vnet-staging (10.20.0.0/16)`
- **Subnet*** select: `default (10.10.1.0/24)`, `backend (10.10.2.0/24)`
- **Public IP** (tooltip: "Assigns a NAT-routed public address. €3.00 per month.") — **toggle switch**: track `40×20`, radius 10, border+bg `#0078D4` when on / border `#8A8886` bg `#fff` when off; knob 12px circle at `top:3px`, `left:23px` (on) / `left:3px` (off), knob bg `#fff` (on) / `#605E5C` (off), `transition:left .15s`. Adjacent label 13px: **"Enabled — a public address is assigned"** / **"Disabled — private access only"**
- Section **"Inbound ports"** 16px/600 `margin-top:22px` + rule
- Three checkbox rows (`padding:6px 0`, gap 10): box 18px, radius 2, checked bg+border `#0078D4` with white 12px check (sw 2), unchecked bg `#fff` border `#8A8886`; label 14px + description 12px `#605E5C`
  - **Allow SSH** — `TCP 22` (default **on**)
  - **Allow HTTP** — `TCP 80`
  - **Allow HTTPS** — `TCP 443`

**Step 4 — Advanced**
- Heading **"Cloud-init user data"**; caption: *"Runs on first boot. YAML #cloud-config or a shell script."*
- `<textarea rows="10">`, `width:100%;max-width:620px`, border `#8A8886`, radius 2, `padding:10px`, mono 12.5px lh 1.5, `resize:vertical`, focus border `#0078D4`; placeholder is multiline: `#cloud-config` / `packages:` / `  - nginx`

**Step 5 — Tags**
- Heading **"Tags"**; caption: *"Name/value pairs for organizing and cost reporting. Tags apply to the VM and its disks."*
- Existing tags as chips: `background:#F3F2F1;border:1px solid #EDEBE9;radius 2;padding:4px 10px`, text `{k} : {v}` 13px, with a 12px ✕ remove button (`#605E5C` → hover `#D13438`, `title="Remove tag"`)
- Add row `margin-top:12px;gap:8px`: input **Name** (160px) + input **Value** (160px) + secondary **Add** button (32px, `#fff`, border `#8A8886`, 13px)
- Default tag present: `env : prod`

**Step 6 — Review + create**
- **Success banner** (when valid): `background:#DFF6DD;border:1px solid #107C10;radius 2;padding:10px 12px;font-size:13px;margin-bottom:18px`, 16px checkC icon `#107C10`, text **"Validation passed"** in `#107C10`/600
- **Error banner** (when invalid): `background:#FDE7E9;border:1px solid #D13438`, same padding; header row with 16px warn triangle `#D13438` and **"Validation failed — fix the following before creating"** in `#A4262C`/600; then `<ul style="margin:8px 0 0 24px;color:#323130">` of errors
- Validation rules (`wizErrors`):
  - `Virtual machine name is required (Basics).`
  - `VM name must start with a lowercase letter and contain only lowercase letters, numbers, and hyphens (Basics).` (regex `^[a-z][a-z0-9-]{0,39}$`)
  - `A resource named "{name}" already exists in this tenant (Basics).`
- **Review groups** — per group: title 14px/600, 1px rule (`margin:6px 0 8px`), then rows `display:flex;font-size:13px;padding:4px 0` with key `flex:0 0 220px;color:#605E5C` and value
  - **Basics**: Project, VM name, Image, SSH public key
  - **Size**: `Size` → `M — 4 vCPU / 8 GB RAM`
  - **Disks**: `OS disk` → `64 GB from {image}`; `Data disks` → comma list `name (128 GB)` or `—`
  - **Networking**: Virtual network, Subnet, Public IP (Yes/No), Inbound ports (comma list or `None`)
  - **Advanced**: `Cloud-init` → `Provided (N lines)` or `—`
  - **Tags**: comma list `k: v` or `—`

**Estimated cost card** (right, sticky) — label "Estimated cost"
- Card `padding:16px`, title 14px/600 mb 12
- Rows `display:flex;justify-content:space-between;font-size:13px;padding:3px 0` — label `#605E5C`, value tabular
  - `Compute (M — 4 vCPU / 8 GB)` → `€36.00`
  - `OS disk (64 GB)` → `€5.12`
  - `Data disks (N GB)` → `€{N×0.08}` (only if data disks)
  - `Public IP address` → `€3.00` (only if enabled)
- 1px rule, then **Monthly total** 14px/600 with tabular total
- Status line `margin-top:12px`, 8px dot + 12px `#605E5C` text:
  - valid → dot `#107C10`, **"Configuration is valid"**
  - invalid → dot `#D13438`, **"N issue(s) to resolve before create"**

**Sticky wizard footer**
`position:sticky;bottom:0;background:#fff;border-top:1px solid #EDEBE9;margin:28px -32px 0;padding:12px 32px;display:flex;gap:8px;align-items:center;z-index:5`
- **Primary**: 32px, `#0078D4`, white, `border:1px solid transparent`, radius 2, 14px/600, `padding:0 20px`; hover `#106EBE`, active `#005A9E`. Label = **"Review + create"** on tabs 0–5, **"Create"** on tab 6.
- **`< Previous`**: secondary 32px, `#fff`, border `#8A8886`, `padding:0 16px`; disabled on tab 0 with text color `#A19F9D` (else `#323130`)
- **`Next : {nextTabName} >`**: secondary, shown only when `tab < 6`

---

### 3.4 Deployment (`isDeploy`) — label "Deployment"
`padding:20px 32px 40px;max-width:900px`. Breadcrumb `Home > deploy-{name}`.

**Header** `display:flex;align-items:center;gap:14px;margin:8px 0 4px`
- Icon 34px: **spinner** while running, **checkC `#107C10`** (sw 1.1) when done
- Title 22px/600: **"Deployment is in progress"** → **"Your deployment is complete"**
- Subtitle 13px `#605E5C`: `Deployment name: deploy-{name} · Tenant: {tenant} · Project: {project}`

**"Deployment details" card** (`margin-top:20px`, card shadow); title `padding:14px 16px 10px` 14px/600
- Table 13px; headers `padding:6px 16px|12px;border-bottom:1px solid #EDEBE9` (no bg): **Resource | Type | Status**
- Rows height 40px, `border-bottom:1px solid #F3F2F1`; Resource cell = per-status icon + label; Type `#605E5C`; **Status text colored by `statusColor`**
- Status icons: `Pending` → 15px clock `#A19F9D`; `Creating` → 15px spinner; `Created` → 15px checkC `#107C10`
- Items generated: `{name}` / Virtual machine; `{name}-osdisk` / Disk; `{name}-nic` / Network interface; `{name}-ip` / Public IP address (only if public IP)
- Sequencing: item *i* → `Creating` at `400 + i*deploySpeed`, → `Created` at `+1.4×deploySpeed`

**Footer actions** `margin-top:20px;gap:8px`
- When done: primary **"Go to resource"** + secondary **"Create another VM"**
- Always: 12px `#605E5C` note **"You can safely leave this page — progress stays available in the notification bell."**

---

### 3.5 VM detail (`isVm`) — label "VM detail"
`display:flex;flex-direction:column;min-height:100%`

**Header block** `padding:20px 32px 0`
- Breadcrumb `Home > Virtual machines > {vm}`
- Title row `gap:12px`: 30px VM product icon; name 24px/600; **status pill** — `display:inline-flex;gap:6px;font-size:12px;border:1px solid #EDEBE9;background:#fff;border-radius:2px;padding:3px 8px` with an 8px status dot
- Subtitle 12px `#605E5C`: `Virtual machine · {project}`

**Command bar** `display:flex;align-items:center;margin-top:12px;border-bottom:1px solid #EDEBE9`
Buttons: `height:36px;padding:0 10px;font-size:13px;gap:6px`, transparent, hover `#F3F2F1`, disabled color `#A19F9D`. Separators: `width:1px;height:18px;background:#EDEBE9;margin:0 4px;align-self:center` rendered **before** flagged items.
| Label | Icon | Enabled when | Separator before |
|---|---|---|---|
| **Connect** | console (terminal) | Running | — |
| **Start** | filled play triangle | Stopped | ✓ |
| **Restart** | restart arrow | Running | — |
| **Stop** | filled square | Running | — |
| **Delete** | trash | not busy | ✓ |
| **Refresh** | restart arrow | always | ✓ |
| **···** | (none, text) | always | — |
Connect → info toast *"Opening web console — noVNC session for {vm} opens in a new tab."*; ··· → *"More actions — Resize, move project, and clone live here in the full product."*
Actions transition status: Start→`Starting`→`Running`, Stop→`Stopping`→`Stopped`, Restart→`Restarting`→`Running` after **1800ms**, then a success toast + notification.

**Body** `display:flex;flex:1;align-items:stretch`

**Left resource menu** `flex:0 0 210px;border-right:1px solid #EDEBE9;background:#fff;padding:10px 0` — label "Resource menu"
- Filter input at top: `padding:0 10px 8px` wrapper, input `height:28px;font-size:12px`, placeholder **"Search (Ctrl+/)"**
- Group headers (hidden when filtered out): `padding:10px 12px 4px;font-size:11px;font-weight:600;color:#605E5C;text-transform:uppercase;letter-spacing:.3px`
- Items: `height:32px;padding:0 12px;gap:8px;font-size:13px`, active bg `#DEECF9` with icon `#0078D4`, inactive icon `#605E5C`, hover `#F3F2F1`
- Structure:
  - *(no title)*: **Overview** (grid) · **Activity log** (clock) · **Access control (IAM)** (person) · **Tags** (tag)
  - **SETTINGS**: **Networking** (globe) · **Disks** (disk) · **Snapshots** (camera) · **Size** (resize)
  - **MONITORING**: **Metrics** (chart)

**Right content** `flex:1;min-width:0;padding:18px 28px 40px`

#### 3.5.1 Overview (`vsOverview`, default)
- **Essentials** collapsible: header `border-bottom:1px solid #EDEBE9;padding-bottom:14px`, toggle button 14px/600 with 12px chevron rotated `0deg` (open) / `-90deg` (closed), `transition:transform .15s`; right side **"JSON view"** link (13px)
- Open body: `display:grid;grid-template-columns:1fr 1fr;gap:0 40px;margin-top:12px`; each row `font-size:13px;padding:3px 0` with key `flex:0 0 130px;color:#605E5C`
  - Left: **Status**, **Project**, **Node**, **Size**
  - Right: **Public IP** (with copy button), **Private IP** (with copy button), **OS image**, **Created**
  - Copy button: 12px two-rectangle icon, `#605E5C` → hover `#0078D4`, `title="Copy"`; fires info toast *"Copied to clipboard"*
- **Chart tiles**: `grid-template-columns:repeat(auto-fit,minmax(220px,1fr));gap:12px;margin-top:18px`; each card `padding:12px 14px` with a header row (label 12px `#605E5C`, value 12px/600 `#323130` tabular) and a **48px-tall sparkline** below (`margin-top:8px`)
  - `CPU (average)` **12%** · `Memory used` **58%** · `Network (in/out)` **3.2 MB/s**
- **"Recent activity on this resource"** 14px/600 `margin:22px 0 8px`; card `#fff` border `#EDEBE9`; rows `padding:9px 14px;border-bottom:1px solid #F3F2F1;font-size:13px;gap:10px`: 8px status dot, action (`flex:1`), actor `#605E5C`, time `flex:0 0 80px;text-align:right;color:#605E5C`
  - `Restart virtual machine / alex@aurora-labs.io / 2 h ago / Succeeded / green`
  - `Create snapshot / ci-bot / Yesterday / Succeeded / green`
  - `Update tags / dana@aurora-labs.io / 3 d ago / Succeeded / green`
  - `Deallocate virtual machine / alex@aurora-labs.io / Last week / Failed / #D13438`

#### 3.5.2 Activity log (`vsActivity`)
Heading 16px/600 mb 12. Full-bleed table `background:#fff;border:1px solid #EDEBE9`, headers `padding:8px 12px;border-bottom:1px solid #EDEBE9;background:#F3F2F1;font-weight:600`.
Columns: **Operation | Initiated by | Status | Time**. Status cell = 8px dot + text. Rows height 40px.

#### 3.5.3 Access control (IAM) (`vsIam`)
Heading 16px/600 mb 4; caption 12px `#605E5C` mb 12: *"Role assignments at this resource's scope. Tenant-level assignments are inherited."*
Table (grey headers) columns: **Name | Type | Role | Scope**
- `Alex Meyer / User / Owner / Tenant (inherited)`
- `ci-bot / Service account / Contributor / Project: web-prod`
- `Dana Okafor / User / Reader / Project: web-prod`

#### 3.5.4 Tags (`vsTags`)
Heading 16px/600 mb 12. Chip row `flex-wrap;gap:8px;margin-bottom:16px` — chip `background:#F3F2F1;border:1px solid #EDEBE9;radius 2;padding:5px 10px;font-size:13px` reading `{k} : {v}` with an 11px ✕ (hover `#D13438`). Add row: **Name** input (160px) + **Value** input (160px) + **Add** secondary button.
Default for `web-prod-01`: `env : prod`, `owner : alex`.

#### 3.5.5 Networking (`vsNetworking`)
Heading 16px/600. Detail card `#fff` border `#EDEBE9` radius 2 `padding:12px 14px;margin-bottom:18px;max-width:560px` — rows 13px `padding:3px 0`, key `flex:0 0 160px;color:#605E5C`:
`Network interface` = `{vm}-nic`; `Virtual network` = `vnet-web-prod`; `Subnet` = `default (10.10.1.0/24)`; `Private IP`; `Public IP`.
Then **"Inbound firewall rules"** 14px/600 mb 8, grey-header table:
Columns **Priority | Name | Port | Protocol | Source | Action** (row height 38px; Action colored)
- `100 / allow-ssh / 22 / TCP / Any / Allow` (green)
- `110 / allow-https / 443 / TCP / Any / Allow` (green)
- `65000 / deny-all-inbound / Any / Any / Any / Deny` (`#D13438`)

#### 3.5.6 Disks (`vsDisks`)
Heading 16px/600. Grey-header table: **Name | Type | Size | State**; State cell = green 8px dot + text.
- `{vm}-osdisk / OS disk / 64 GB / Attached`
- (only for `web-prod-01`) `web-prod-01-data / Data disk / 256 GB / Attached`

#### 3.5.7 Snapshots (`vsSnapshots`)
Header row `justify-content:space-between;margin-bottom:12px`: heading 16px/600 + primary **"Take snapshot"** button (32px, `padding:0 14px`, 13px/600).
- **Populated**: grey-header table **Name | Created | Size**
- **Empty state** (default — `snaps` starts empty): card `#fff` border `#EDEBE9` radius 2 `padding:40px;text-align:center`
  - 40px camera icon, stroke `#C8C6C4`, stroke-width 1, `margin-bottom:12px`
  - Title 14px/600: **"No snapshots yet"**
  - Body 13px `#605E5C` `margin:6px 0 16px`: *"Snapshots capture the VM's disks at a point in time so you can roll back after risky changes."*
  - Primary CTA 32px `padding:0 20px` 14px/600: **"Take your first snapshot"**
- Taking a snapshot creates `{vm}-snap-{n}` / `Just now` / `64 GB`, fires info toast, then a notification after 2000ms.

#### 3.5.8 Size (`vsSize`)
Heading 16px/600 mb 4; caption 12px `#605E5C` mb 14: *"Resizing restarts the virtual machine. The current size is highlighted."*
Same size-card grid as wizard step 1 but `max-width:720px` and body text `#323130` (no explicit color). Selecting sets status `Resizing` for 2000ms then `Running` + toast *"Resize complete — {vm} is now size {S}."*

#### 3.5.9 Metrics (`vsMetrics`)
Heading: **"Metrics"** 16px/600 with a trailing 12px, weight-400, `#605E5C` span **"· Last hour · 1-minute granularity"**.
Grid `repeat(auto-fit,minmax(280px,1fr));gap:12px`; cards `padding:14px 16px`, header row 13px (label `#605E5C`, value 600 `#323130` tabular), **90px-tall sparkline** `margin-top:10px`.
- `CPU (average)` **12%** · `Memory used` **58%** · `Disk IOPS` **340** · `Network (in/out)` **3.2 MB/s**

---

### 3.6 All resources / filtered lists (`isResources`) — label "All resources"
`padding:20px 32px 40px;max-width:1360px`. Breadcrumb `Home > {title}`; H1 `{title}` 24px/600 mb 12.
Titles by `resType`: `all → All resources`, `vm → Virtual machines`, `k8s → Kubernetes clusters`, `db → Databases`, `net → Networking`, `storage → Storage`.

**Command bar** `display:flex;align-items:center;border-bottom:1px solid #EDEBE9;margin-bottom:12px` — buttons 36px, `padding:0 10px`, 13px, gap 6, hover `#F3F2F1`
- **Create** (14px blue plus icon `#0078D4`) → catalog
- **Refresh** (restart arrow) → reseeds charts
- separator (1×18 `#EDEBE9`)
- **Manage view** (columns icon: rect with two vertical lines) → help toast

**Filter pills** `display:flex;gap:8px;flex-wrap:wrap;margin-bottom:12px`
Pill: `height:26px;padding:0 10px;font-size:12px;background:#fff;border:1px dashed #C8C6C4;border-radius:13px;color:#323130`, hover `border-color:#0078D4`
- `Project == all` (or the project name) → opens tenant pane
- `Type == all` / `Type == Virtual machines  ✕` → clears type filter
- `+ Add filter` → info toast *"Filters — Tag and status filters arrive in a later iteration."*

**Bulk selection bar** (when ≥1 selected): `background:#EFF6FC;border:1px solid #DEECF9;border-radius:2px;padding:4px 10px;margin-bottom:10px;font-size:13px;gap:4px`
- `{n} selected` 600, `margin-right:8px`
- **Start** / **Stop** (link-buttons 28px, `#0078D4`), **Delete** (`#D13438`), **Clear selection** (`#605E5C`, `margin-left:auto`); all hover `#DEECF9`
- Bulk Delete deliberately errors: *"Bulk delete — Destructive bulk actions require type-to-confirm per resource."* (err toast)

**Table** in a `#fff` card with border + card shadow. Headers: `background:#F3F2F1;border-bottom:1px solid #EDEBE9;padding:8px 8px|12px;font-weight:600`.
Columns: **☐ (40px) | Name | Type | Project | Status | Tags**
- Checkbox: 16×16 button, radius 2, checked bg+border `#0078D4` with white 11px check (sw 2.2), unchecked border `#8A8886` bg `#fff`, `title="Select row"`
- Row: `border-bottom:1px solid #F3F2F1`, `background:{#EFF6FC when selected, else transparent}`, cell height 40px
- Name: link + 18px product icon (gap 8)
- Status: 8px dot + text (dot color from `statusColor`)
- Tags: inline chips `background:#F3F2F1;border:1px solid #EDEBE9;radius 2;padding:2px 8px;font-size:11px;margin-right:6px`

Seed data (10 resources):
| Name | Type | Project | Status | Tags |
|---|---|---|---|---|
| web-prod-01 | Virtual machine | web-prod | Running | `env: prod` |
| gitlab-runner-02 | Virtual machine | web-prod | Running | `team: ci` |
| pg-primary | Virtual machine | data-staging | Stopped | — |
| win-jump-01 | Virtual machine | sandbox | Provisioning | — |
| apps-prod | Kubernetes cluster | web-prod | Healthy | `env: prod` |
| dev-cluster | Kubernetes cluster | sandbox | Healthy | — |
| orders-db | PostgreSQL 16 | web-prod | Available | `env: prod` |
| cache-prod | Redis 7 | web-prod | Available | — |
| vnet-web-prod | Virtual network | web-prod | Active | — |
| data-lake | S3 bucket | data-staging | Active | — |

Non-VM rows fire an info toast: *"Same anatomy — {type} pages reuse the VM resource-page anatomy — command bar, Essentials, menu."*

---

### 3.7 Activity log (`isActivity`) — label "Activity log"
`padding:20px 32px 40px;max-width:1360px`. Breadcrumb; H1 **"Activity log"** 24px/600 mb 12; caption 12px `#605E5C` mb 12: *"All control-plane operations in tenant {tenant} · last 7 days"*.
Card with grey-header table. Columns: **Operation | Resource | Initiated by | Status | Time** (Operation/Time padded 16px, rest 12px). Resource is a link; Status = dot + text; row height 40px.
Rows:
| Operation | Resource | Initiated by | Status | Time |
|---|---|---|---|---|
| Create virtual machine | win-jump-01 | alex@aurora-labs.io | In progress (`#0078D4`) | Today 08:12 |
| Restart virtual machine | web-prod-01 | alex@aurora-labs.io | Succeeded (`#107C10`) | Today 07:44 |
| Scale node pool | apps-prod | ci-bot | Succeeded | Yesterday 22:10 |
| Create snapshot | web-prod-01 | ci-bot | Succeeded | Yesterday 03:00 |
| Update firewall rules | vnet-web-prod | dana@aurora-labs.io | Succeeded | Mon 16:31 |
| Deallocate virtual machine | pg-primary | alex@aurora-labs.io | Failed (`#D13438`) | Last week |

---

### 3.8 Placeholder / "coming later" (`isPlaceholder`) — label "Placeholder"
`padding:80px 32px;display:flex;flex-direction:column;align-items:center;text-align:center`
- 44px gear icon, stroke `#C8C6C4`, sw 1, `margin-bottom:14px`
- Title 18px/600 `{phTitle}`; body 13px `#605E5C` `margin:8px 0 18px;max-width:440px;line-height:1.5`
- Primary button **"Go to dashboard"**
Two known instances:
- **"Portal settings"** — *"Theme, language, and default project settings arrive in a later iteration of this prototype."* (from top-bar gear + nav Settings)
- **"Access control (IAM)"** — *"Tenant-scope role assignments follow the same pattern as resource IAM — open web-prod-01 → Access control (IAM) to see the designed table."*

---

### 3.9 Landing page (`isLanding`) — label "Landing page"
`flex:1;overflow-y:auto;background:#fff`
- **Header** 56px, `background:#1B1A19`, `padding:0 40px;gap:10px`: 22px logo + `Proxcloud` 16px/600 white; right cluster gap 20: **"Sign in"** text link (white, hover `#C8C6C4`) + primary **"Get started"** button (32px, `padding:0 18px`, 13px/600)
- **Hero** `max-width:920px;margin:0 auto;padding:88px 40px 72px;text-align:center`
  - Eyebrow badge: `display:inline-flex;gap:8px;font-size:12px;font-weight:600;color:#0078D4;background:#EFF6FC;border:1px solid #DEECF9;border-radius:2px;padding:4px 10px;margin-bottom:22px` — **"Private cloud console for Proxmox VE"**
  - H1 44px/600, lh 1.15, ls -.5px, `text-wrap:balance`: **"Self-service cloud, on your own metal"**
  - Paragraph 17px `#605E5C` lh 1.55 `max-width:620px;margin:18px auto 0`, `text-wrap:pretty`: *"Proxcloud turns a Proxmox VE cluster into a multi-tenant cloud portal. Teams provision virtual machines, Kubernetes clusters, databases, networks, and storage in minutes — without ever seeing the substrate."*
  - CTA row gap 10, centered, `margin-top:30px`: **"Get started"** (38px, `padding:0 26px`, 14px/600, blue, hover `#106EBE`, active `#005A9E`) + **"Explore the console"** (38px secondary, `padding:0 22px`, → home)
- **Service strip** `background:#FAF9F8;border-top/bottom:1px solid #EDEBE9;padding:44px 40px`; inner `max-width:1060px;grid:repeat(auto-fit,minmax(180px,1fr));gap:24px`; each cell column `gap:9px`: 26px product icon, name 14px/600, desc 12.5px `#605E5C` lh 1.5
  - **Virtual machines** — "Templates, T-shirt sizes, cloud-init, snapshots, and a web console."
  - **Kubernetes** — "Managed K3s with node pools, upgrades, and kubeconfig download."
  - **Databases** — "PostgreSQL, MongoDB, and Redis with automated backups."
  - **Networking** — "Isolated VXLAN networks, firewall rules, and load balancers."
  - **Storage** — "Block volumes and S3-compatible buckets, attach and go."
- **Feature trio** `max-width:1060px;padding:56px 40px;grid:repeat(auto-fit,minmax(240px,1fr));gap:36px`; each: 20px blue line icon (`#0078D4`, sw 1.3, `margin-bottom:10px`), title 15px/600 mb 6, body 13px `#605E5C` lh 1.55
  - person icon — **"Hard tenant isolation"** — *"Each tenant gets its own users, virtual networks, quotas, and activity log. Owner, Contributor, and Reader roles inherit from tenant to project, Azure-style."*
  - chart icon — **"Quotas and flat-rate cost"** — *"vCPU, RAM, and storage limits per tenant and project, with a live monthly estimate on every create flow. No surprise bills — it is your hardware."*
  - bolt icon — **"Everything is async"** — *"Provisioning never blocks. Progress lives in the notification center, every action is audited, and every view is deep-linkable."*
- **Footer** `border-top:1px solid #EDEBE9;padding:20px 40px;gap:16px;font-size:12px;color:#605E5C`: left **"Proxcloud — runs on your Proxmox VE cluster"**; right link group gap 16: **Docs · Status · Privacy**

---

### 3.10 Sign in (`isSignin`) — label "Sign in"
Page `background:#FAF9F8`, column; card area centered `padding:40px 20px`.
**Card**: `width:400px;background:#fff;border:1px solid #EDEBE9;border-radius:2px;box-shadow:0 2px 6px rgba(0,0,0,.13);padding:40px`
- Brand row gap 9 mb 22: 24px logo + `Proxcloud` 16px/600
- **Step 1 — email**
  - H2 **"Sign in"** 24px/600 mb 16
  - Input: **underline style** — `width:100%;height:34px;border:none;border-bottom:1px solid #8A8886;padding:0 2px;font-size:15px;background:transparent`, focus `border-bottom-color:#0078D4`, placeholder **"Email address"**, `autofocus`
  - Error (if any) 12px `#A4262C` `margin-top:6px` — e.g. *"Enter a valid email address, like alex@aurora-labs.io."*
  - 13px `#605E5C` `margin-top:16px`: `No account? ` + link **"Request a tenant"**
  - Right-aligned primary **"Next"** (32px, `padding:0 32px`) `margin-top:26px`
- **Step 2 — password**
  - Back button: chevron-left 12px + the entered email, 13px `#605E5C`, hover `#323130`, mb 14
  - H2 **"Enter password"** 24px/600 mb 16
  - `type="password"` underline input, placeholder **"Password"**, autofocus
  - Link **"Forgot password?"** 13px `margin-top:16px`
  - Right-aligned primary **"Sign in"** (32px, `padding:0 28px`)
- **Below the card** (`padding:0 20px 32px`, width 400):
  - **SSO button** — full-width 44px, `#fff`, border `#EDEBE9`, radius 2, shadow `0 2px 6px rgba(0,0,0,.13)`, 14px, gap 10, `padding:0 18px`, person icon `#605E5C`: **"Sign in with SSO (OIDC)"**
  - Centered 12px `#605E5C` `margin-top:18px` link: **"← Back to proxcloud.example"**

Successful sign-in → home + toast *"Welcome back, Alex — Signed in to tenant aurora-labs as {email}."*

---

### 3.11 Request a tenant / sign-up (`isSignup`) — label "Request a tenant"
Centered on `#FAF9F8`, card `width:440px`, same card treatment, `padding:40px`.
- Brand row (24px logo + wordmark)
- H2 **"Request a tenant"** 24px/600
- Body 13px `#605E5C` lh 1.5 `margin:8px 0 22px`: *"Tenants are created by the platform operator. Tell us who you are and we will set up your isolation boundary."*
- Stacked labelled fields — label 13px/600 `margin-bottom:5px`, input `width:100%;height:32px;border:1px solid #8A8886;radius 2;padding:0 8px;font-size:14px`, `margin-bottom:16px`
  - **Full name** — placeholder `e.g. Alex Meyer`
  - **Work email** — placeholder `you@company.com`
  - **Tenant name** — placeholder `e.g. aurora-labs` (`margin-bottom:8px`) + hint 12px `#605E5C` mb 22: *"Lowercase letters, numbers, and hyphens. This becomes your isolation boundary."*
- Footer row `justify-content:space-between`: link **"Already have access? Sign in"** (13px) + primary **"Request access"** (32px, `padding:0 22px`)
- Submit → back to sign-in + toast *"Request submitted — The platform operator will review your tenant request for "{org}"."*

---

### 3.12 Tenant + project pane (`tenOpen`) — label "Tenant + project pane"
400px pane. Title **"Tenant + project filter"**.
- Intro 12px `#605E5C` lh 1.5 mb 14: *"The tenant is your isolation boundary. Everything you see — resources, networks, quotas, costs — is scoped to it."*
- Search input 32px, placeholder **"Search tenants and projects"**, mb 14
- Section label **"TENANTS"** — 12px/600 `#605E5C` uppercase ls .3px mb 6
- Tenant row button: `padding:9px 10px;gap:10px;border-radius:2px`, active bg `#EFF6FC`, hover `#F3F2F1`; **radio**: 16px circle, `border:1px solid {#0078D4 active | #8A8886}`, inner 8px `#0078D4` dot when active; text stack: name 13px/600, domain 11px `#605E5C`
  - `aurora-labs` / aurora-labs.io (projects: web-prod, data-staging, sandbox)
  - `helios GmbH` / helios-gmbh.example (erp-prod, iot-staging)
  - `platform-internal` / ops.proxcloud.local (bootstrap)
- 1px rule `margin:14px 0`
- Section label **"PROJECTS IN {tenant}"**; rows `padding:8px 10px`, same radio, single-line 13px name. First entry is **"All projects"**.
- Footer: primary **"Done"** (left) + **"Sign out"** link (right)
Switching tenant resets project to "All projects", routes home, info toast *"Switched tenant — You are now working in {t}. Everything you see is scoped to it."*

---

### 3.13 Notifications pane (`notifOpen`) — label "Notifications pane"
400px pane. Title **"Notifications"**.
- Item card: `border:1px solid #EDEBE9;border-radius:2px;padding:12px;margin-bottom:10px`; inner `display:flex;gap:10px;align-items:flex-start`
  - Icon 16px by kind: `prog` → **spinner**; `ok` → checkC `#107C10`; `err` → warn `#D13438`
  - Title 13px/600; desc 12px `#605E5C` lh 1.45 `margin-top:3px`
  - **Progress bar** (kind `prog`): `height:4px;background:#F3F2F1;border-radius:2px;margin-top:8px` with `#0078D4` fill, `transition:width .4s`
  - Timestamp 11px `#A19F9D` `margin-top:6px`
- Empty state: centered 13px `#605E5C` `padding:40px 0` — **"No new notifications."**
- Footer: secondary **"Dismiss all"** (32px, `padding:0 16px`, 13px)
Seed notifications:
1. **"Provisioning win-jump-01"** — *"Virtual machine deployment in progress in project sandbox."* — 5 min ago — prog 62%
2. **"Snapshot completed"** — *"apps-prod · etcd-backup finished successfully."* — 2 h ago — ok

Unread starts at **2**; opening the pane zeroes the badge.

---

### 3.14 Delete pane (`delOpen`) — label "Delete pane" — **typed-name confirmation**
400px pane. Title **"Delete virtual machine"**.
- **Danger callout**: `display:flex;gap:10px;background:#FDE7E9;border:1px solid #D13438;border-radius:2px;padding:10px 12px;font-size:13px;line-height:1.5;margin-bottom:16px` with a 16px warn triangle (`#D13438`, `flex-shrink:0;margin-top:2px`) and text `Deleting `**`{vm}`**` is permanent and cannot be undone.`
- **"This will also delete"** 13px/600 mb 6, then `<ul style="font-size:13px;color:#605E5C;margin:0 0 18px 18px;line-height:1.7">`:
  - "All attached disks and their snapshots"
  - "The network interface and its private IP reservation"
  - "Any public IP assigned to this VM"
- Prompt 13px mb 6: `Type `**`{vm}`**` to confirm`
- Input full-width 32px, border `#8A8886`, **placeholder = the VM name**
- Footer `display:flex;gap:8px`:
  - **Delete** — 32px, `padding:0 20px`, white text, `border:1px solid transparent`, radius 2, 14px/600; background is **`#D13438` only when `delText === vmName`, otherwise `#F3F2F1`**; `disabled` until exact match
  - **Cancel** — secondary
On confirm: pane closes, route → home, resource removed, info toast *"Deleting {vm} — The virtual machine and its resources are being removed."*, then after 2400ms a notification *"Deleted virtual machine — {vm} and its disks, NIC, and public IP were removed."* + toast *"Delete complete"*.

---

### 3.15 JSON view pane (`jsonOpen`) — label "JSON view pane"
**440px** pane. Title **"Resource JSON"**. Body `overflow:auto`.
`<pre style="background:#F3F2F1;border:1px solid #EDEBE9;border-radius:2px;padding:12px;font-size:12px;font-family:'Cascadia Code',Consolas,monospace;line-height:1.5;white-space:pre-wrap">`
Payload shape: `{ name, type:"proxcloud/virtual-machine", tenant, project, status, node, size, publicIp, privateIp, image, created, tags:{k:v} }` (2-space indent).

---

### 3.16 Command palette (`palOpen`) — label "Command palette"
- Scrim: `position:fixed;inset:0;background:rgba(0,0,0,.25);z-index:60;display:flex;justify-content:center;align-items:flex-start;padding-top:110px`; click closes
- Dialog: `width:600px;max-width:90vw;background:#fff;border-radius:2px;box-shadow:0 12px 40px rgba(0,0,0,.3);overflow:hidden`; click stops propagation
- Search row: `padding:0 14px;gap:10px;border-bottom:1px solid #EDEBE9` — 15px magnifier `#605E5C`, borderless input `height:44px;font-size:14px` with placeholder **`Search resources, or type "create vm"`**, and an **Esc** chip: 11px `#A19F9D`, `border:1px solid #EDEBE9`, radius 2, `padding:2px 6px`
- Results: `max-height:340px;overflow-y:auto;padding:6px 0`; row button `padding:9px 14px;gap:10px`, hover `#F3F2F1`; icon, label 13px (`flex:1`), hint 11px `#A19F9D`
- Fixed quick actions (filtered by query):
  - bolt `#0078D4` — **"Create a virtual machine"** — hint `Quick create`
  - bolt `#0078D4` — **"Create a Kubernetes cluster"** — hint `Catalog`
  - grid `#605E5C` — **"All resources"** — hint `Browse`
- Plus up to 5 matching resources: product icon, resource name, hint `{type} · {project}`

---

## 4. COMPONENT PATTERNS

### 4.1 Buttons
| Variant | Exact style |
|---|---|
| **Primary** | `height:32px;padding:0 20px;background:#0078D4;color:#fff;border:1px solid transparent;border-radius:2px;font-size:14px;font-weight:600;cursor:pointer` · hover `background:#106EBE` · active `background:#005A9E` |
| Primary (compact) | same but `padding:0 14px;font-size:13px` (e.g. "Take snapshot") |
| Primary (large / landing) | `height:38px;padding:0 26px;font-size:14px;font-weight:600` |
| **Secondary** | `height:32px;padding:0 16px;background:#fff;color:#323130;border:1px solid #8A8886;border-radius:2px;font-size:14px` · hover `background:#F3F2F1` |
| Secondary (compact) | `padding:0 14px;font-size:13px` (e.g. "Add", "Dismiss all") |
| **Danger** | `height:32px;padding:0 20px;background:#D13438;color:#fff;border:1px solid transparent;font-weight:600` — background degrades to `#F3F2F1` while `disabled` |
| **Command-bar / toolbar** | `height:36px;padding:0 10px;background:none;border:none;font-size:13px;gap:6px;color:#323130` (disabled `#A19F9D`) · hover `background:#F3F2F1` |
| **Link-button** | `background:none;border:none;color:#0078D4;font-size:13px` · hover `color:#005A9E` (e.g. "Add a data disk (128 GB)") |
| **Icon button** | `background:none;border:none;padding:2–6px;color:#605E5C` · hover `#0078D4` (copy) or `#D13438` (delete) or `#323130` (close) |
| **Nav item** | `height:36px;width:100%;background:{active #DEECF9 | transparent};border:none;font-size:13px;text-align:left` · hover `#F3F2F1` |

Note: the **Previous** button in the wizard is styled secondary but only its **text color** changes when disabled (`#A19F9D`), not its border.

### 4.2 Inputs / selects / textarea
- **Standard input**: `height:32px;border:1px solid #8A8886;border-radius:2px;padding:0 8px;font-size:14px;outline:none;background:#fff`, focus → `border-color:#0078D4`
- Search variants use `padding:0 10px`; the resource-menu filter uses `height:28px;font-size:12px`
- **Select**: identical but `padding:0 6px` and no explicit focus rule beyond `outline:none`
- **Textarea**: `border:1px solid #8A8886;border-radius:2px;padding:10px;font-family:'Cascadia Code',Consolas,monospace;font-size:12.5px;line-height:1.5;resize:vertical`, focus `border-color:#0078D4`
- **Underline input** (auth only): `height:34px;border:none;border-bottom:1px solid #8A8886;padding:0 2px;font-size:15px;background:transparent`, focus `border-bottom-color:#0078D4`
- **Invalid**: border `#D13438`; message 12px `#A4262C` below, `max-width:300px`
- **Required marker**: `<span style="color:#A4262C">*</span>` after the label
- **Placeholder color** `#A19F9D` (global rule)

### 4.3 Tables
Two header treatments:
- **Embedded-in-card tables** (dashboard "Recent resources", deployment details, wizard disks): `th{text-align:left;font-weight:600;padding:6px 16px|12px|8px;border-bottom:1px solid #EDEBE9}` — **transparent** background
- **List-page tables** (all resources, activity log, all VM sub-blades): same plus `background:#F3F2F1`, padding `8px 12px|16px`
Body rows: `border-bottom:1px solid #F3F2F1`; the first cell carries `height:40px` (38px for firewall). Cell padding `0 8|12|16px`. Secondary columns use `color:#605E5C`. **No hover state is defined on table rows** — the only row state is `background:#EFF6FC` when the row is checkbox-selected. **No sorting affordances exist** (no sort arrows, no clickable headers).

### 4.4 Tabs
- **Wizard tabs** (underline style): container `display:flex;gap:2px;border-bottom:1px solid #EDEBE9`; button `padding:9px 12px;font-size:14px;border-bottom:2px solid {#0078D4 | transparent};margin-bottom:-1px`; states — active `#323130`/600, idle `#605E5C`/400, locked `#A19F9D` + `disabled`
- **VM blade "tabs"** are actually the 210px left resource menu (see 3.5), not a tab strip

### 4.5 Progress bars
- Quota bars & notification progress: track `height:4px;background:#F3F2F1;border-radius:2px`; fill `height:4px;border-radius:2px;background:#0078D4;width:{pct}%` (notification fill adds `transition:width .4s`)

### 4.6 Status dots & pills
- **Dot**: `width:8px;height:8px;border-radius:50%;background:{statusColor}`; usually inside `display:inline-flex;align-items:center;gap:6px` with the label text
- **Status pill** (VM header only): `display:inline-flex;align-items:center;gap:6px;font-size:12px;border:1px solid #EDEBE9;background:#fff;border-radius:2px;padding:3px 8px` + dot + status text
- **Tag chips**: `background:#F3F2F1;border:1px solid #EDEBE9;border-radius:2px` — `padding:2px 8px;font-size:11px` (table), `padding:4px 10px;font-size:13px` (wizard), `padding:5px 10px;font-size:13px` (VM tags blade)
- **Filter pill**: `height:26px;padding:0 10px;font-size:12px;border:1px dashed #C8C6C4;border-radius:13px`, hover `border-color:#0078D4`
- **Operator badge**: uppercase 10px/600 ls .4px on `#FFB900`
- **Notification badge**: `min-width:14px;height:14px;border-radius:7px;background:#0078D4;color:#fff;font-size:10px;font-weight:600;padding:0 3px`

### 4.7 Checkbox / radio / toggle
- **Checkbox (form)**: 18×18, radius 2, checked `background:#0078D4;border-color:#0078D4` with white 12px check path `M3 8.5l3.2 3L13 4.5` sw 2; unchecked `#fff` / `#8A8886`
- **Checkbox (table)**: 16×16, same colors, check 11px sw 2.2
- **Radio (pane)**: 16px circle, 1px border (`#0078D4` selected / `#8A8886`), inner 8px `#0078D4` dot
- **Toggle**: track 40×20, radius 10; on = `background:#0078D4;border:1px solid #0078D4`; off = `background:#fff;border:1px solid #8A8886`; knob 12px circle at `top:3px`, `left:23px|3px`, `background:#fff|#605E5C`; `transition:left .15s`, `transition:background .15s`

### 4.8 Selection card (size picker)
`text-align:left;border-radius:2px;padding:14px;cursor:pointer`
- Selected: `background:#EFF6FC`, `border:1px solid #0078D4`, `box-shadow:inset 0 0 0 1px #0078D4`, checkC icon `#0078D4` 16px
- Unselected: `background:#fff`, `border:1px solid #C8C6C4`, `box-shadow:none`, no icon
- Hover: `border-color:#0078D4`

### 4.9 Banners / callouts
| Type | Style |
|---|---|
| Success | `background:#DFF6DD;border:1px solid #107C10;border-radius:2px;padding:10px 12px;font-size:13px` + 16px checkC `#107C10` + bold `#107C10` text |
| Error | `background:#FDE7E9;border:1px solid #D13438;` same padding + 16px warn triangle `#D13438` + bold `#A4262C` heading + `<ul>` of items in `#323130` |
| Info/eyebrow (landing) | `background:#EFF6FC;border:1px solid #DEECF9;color:#0078D4;font-weight:600;font-size:12px;padding:4px 10px` |
| Bulk-selection bar | `background:#EFF6FC;border:1px solid #DEECF9;padding:4px 10px` |

### 4.10 Confirmation dialogs
Only one destructive confirm exists and it is **type-to-confirm** (see 3.14): red callout + consequence list + "Type `{name}` to confirm" input + Delete button that stays `#F3F2F1` and `disabled` until `delText === vmName` exactly, then flips to `#D13438`. Bulk delete explicitly refuses and points back to per-resource typed confirmation.

### 4.11 Context / row action menus
There is **no dropdown menu component**. Row-level actions are reached by navigating into the resource. The command bar's overflow is a `···` text button that fires an info toast (placeholder for the real menu). The `dots` icon path exists in the icon library (`M3.2 8h.01M8 8h.01M12.8 8h.01`) but is not wired to any rendered element.

### 4.12 Tooltips
Native HTML `title` attributes only — used on: hamburger ("Toggle navigation"), bell ("Notifications"), gear ("Settings"), help ("Help + support"), user chip ("Switch tenant or project"), nav items (their own label, so collapsed nav is readable), pane close ("Close"), copy buttons ("Copy"), delete buttons ("Remove disk" / "Remove tag"), table checkbox ("Select row"), and the small info-circle icons next to wizard field labels which carry the explanatory copy:
- "Projects group resources inside a tenant, like Azure resource groups."
- "1–40 characters: lowercase letters, numbers, and hyphens. Must start with a letter."
- "Injected via cloud-init on first boot."
- "Assigns a NAT-routed public address. €3.00 per month."

### 4.13 Loading / async states
- **Spinner** (`spinner(size, color)`): 16px viewBox, track `circle r=6 stroke:#DEECF9 sw:2`, arc `M8 2a6 6 0 0 1 6 6` stroke `#0078D4` sw 2 round cap, `animation:pcspin 1s linear infinite`. Sizes used: 15 (deployment rows), 16 (notifications), 34 (deployment header)
- **Sparkline** (`sparkEl(seed, height)`): deterministic LCG random walk (`x = x*9301+49297 mod 233280`), 24 points, value clamped 6–92, `viewBox="0 0 100 34"`, `preserveAspectRatio:none`, `width:100%`. Renders: 2 gridlines (`M0 11.3h100M0 22.6h100`, `#F3F2F1`, 0.6), a closed area path filled `#DEECF9` @ `.7`, and the line `#0078D4` @ 1.2 with `vectorEffect:non-scaling-stroke`. Heights: **44** (dashboard cost), **48** (VM overview tiles), **90** (Metrics blade). Refresh/`seed` bump re-randomizes.
- There are **no skeleton loaders**; async is represented via status text + spinner icons + progress bars.

### 4.14 Empty states
Two patterns, both centered:
- Card empty state (snapshots): 40px outline icon `#C8C6C4` sw 1, 14px/600 title, 13px `#605E5C` body, primary CTA — inside `#fff` card `padding:40px`
- Full-page placeholder: 44px icon `#C8C6C4`, 18px/600 title, 13px body `max-width:440px`, primary CTA
- Inline text empty state (notifications): centered 13px `#605E5C`, `padding:40px 0`

---

## 5. ICONOGRAPHY

All icons are **inline SVG**. Two systems plus a few one-offs.

### 5.1 Line icons — `mi(name, size=16, color='#323130', strokeWidth=1.3)`
Single-path, `viewBox="0 0 16 16"`, `fill:none`, `stroke-linecap:round`, `stroke-linejoin:round`, `flexShrink:0`.

| Key | Shape | Where used |
|---|---|---|
| `home` | house outline `M2.5 7.5 8 2.5l5.5 5M4 7v6.5h8V7` | Left nav → Home (`#0078D4`, sw 1.4) |
| `person` | head + shoulders | Nav → Access control (IAM); resource menu → IAM; landing feature 1; SSO button |
| `clock` | circle + hands | Nav → Activity log; resource menu → Activity log; deployment `Pending` status icon (`#A19F9D`) |
| `gear` | 8-spoke gear | Nav → Settings; top-bar Settings; placeholder screen (44px `#C8C6C4`) |
| `grid` | 4 squares | Resource menu → Overview; palette "All resources" |
| `tag` | tag with hole | Resource menu → Tags |
| `globe` | globe with meridians | Resource menu → Networking |
| `disk` | cylinder | Resource menu → Disks |
| `camera` | camera body + lens | Resource menu → Snapshots; snapshots empty state (40px `#C8C6C4`) |
| `resize` | double corner arrows | Resource menu → Size |
| `chart` | axis + line | Resource menu → Metrics; landing feature 2 |
| `play` | triangle (outline variant) | (superseded by filled `fi('play')`) |
| `stop` | square (outline variant) | (superseded by filled `fi('stop')`) |
| `restart` | circular arrow | VM Restart & Refresh; resources Refresh |
| `trash` | bin with lid + 2 lines | VM Delete; wizard disk row delete |
| `console` | terminal window with prompt | VM Connect |
| `dots` | 3 dots | *(defined, unused)* |
| `search` | magnifier | Top-bar search; command palette |
| `check` | check mark | *(inline in checkboxes)* |
| `checkC` | check in circle | Validation passed; deployment done (34px); `Created` rows; selected size cards; ok toasts/notifications |
| `info` | i in circle | Info toasts; wizard field-help tooltips (13px `#605E5C`, drawn inline) |
| `warn` | triangle + `!` | Error banners; delete callout; err toasts/notifications |
| `plus` | `+` | "Create a resource" nav; Create command; add-disk link; "See the catalog" tile (24px, sw 1.2) |
| `bolt` | lightning | Palette quick actions; landing feature 3 |

### 5.2 Filled micro-icons — `fi(name, size=14, color)`
`viewBox="0 0 16 16"`, filled paths only: `play` (`M5.5 3.5v9l7-4.5z`) and `stop` (`M4.5 4.5h7v7h-7z`) — used for the VM Start / Stop command-bar buttons at 13px.

### 5.3 Product / service icons — `svc(type, size=20)`
Multi-shape, filled, `viewBox="0 0 20 20"`, Azure-style multicolor. Types: `vm`, `k8s`, `pg`, `mongo`, `redis`, `net`, `lb`, `vol`, `bucket`, `allres` (fallback). Sizes used: **16** (nav "All resources"), **17** (nav favorites), **18** (table/name rows, recent list, palette results), **24** (catalog cards), **26** (dashboard tiles, landing services), **30** (VM detail header).

### 5.4 One-off inline SVGs (not in either library)
- **Brand hexagon logo** — `viewBox="0 0 20 20"`, 3 paths: body `#0078D4`, top facet `#50E6FF`, left facet `#005BA1`. Sizes: 18 (app top bar), 22 (landing header), 24 (auth cards).
- **Hamburger** — 3 horizontal lines, sw 1.4, white
- **Bell** — outline bell + clapper arc, sw 1.3, white
- **Question-in-circle** (help) — sw 1.3, white
- **Copy** — two overlapping rectangles, 12px, sw 1.3
- **Close ✕** — `M3.5 3.5l9 9M12.5 3.5l-9 9`, sizes 11/12/14, sw 1.4
- **Chevron down** (Essentials toggle) — `M3.5 6l4.5 4.5L12.5 6`, 12px, rotates -90° when collapsed
- **Chevron left** (auth back) — `M10.5 3.5 6 8l4.5 4.5`, 12px
- **Columns** (Manage view) — `M2 2.5h12v11H2zM6.5 2.5v11M11 2.5v11`, 14px

---

## 6. NOTES FOR REBUILD

- **Radius is 2px almost everywhere** — this is the single most identity-defining token. Only pills (13px), the toggle (10px), circles, and the scrollbar deviate.
- Cards are `#fff` + `1px solid #EDEBE9` + the two-layer Fluent depth-4 shadow; hover elevates to depth-8 **only** on clickable cards (dashboard tiles, catalog cards).
- Two distinct table header styles exist (transparent inside dashboard/deployment cards; `#F3F2F1` on list pages) — preserve both.
- Hover background is universally `#F3F2F1`; selected/active background is `#DEECF9` for nav and `#EFF6FC` for rows/cards. Both blues are used, and they are not interchangeable in the original.
- The layout has a hard `min-width:1280px` with horizontal scrolling instead of responsive collapse; only the landing page and auth screens use `auto-fit` grids.
- Every destructive path routes through the typed-name delete pane; there is no `window.confirm`-style dialog anywhere.
- All motion is short (.15–.2s) except the notification progress (.4s) and the 1s spinner.
