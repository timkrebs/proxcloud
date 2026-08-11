Both files decoded and fully read. Here is the report.

---

# Proxcloud design — behavioral layer analysis

Sources:
- Template + logic: `/Users/tim.krebs/.claude/projects/-Users-tim-krebs-Development-Projects-proxcloud/5d7b32ce-1dfb-4b90-ba7e-2ddb94ac5703/tool-results/toolu_01HQWDAMQXrLiAarnZC1MJos.txt` (decodes to `Proxcloud.dc.html`, 1557 lines: template lines 9–970, logic script lines 971–1554)
- Runtime: `/Users/tim.krebs/.claude/projects/-Users-tim-krebs-Development-Projects-proxcloud/5d7b32ce-1dfb-4b90-ba7e-2ddb94ac5703/tool-results/toolu_01UCbmKvwghsoeuBJic3EJQK.txt` (decodes to `support.js`, 1842 lines)

---

## 1. RUNTIME SEMANTICS (x-dc / dc-runtime)

The document is `<x-dc>…</x-dc>` (the template) plus one `<script type="text/x-dc" data-dc-script data-props="…">` (the logic). The runtime compiles the template once into React element builders and renders it against a flat "vals" object.

**Bindings `{{ expr }}`** — *not* JavaScript. `resolve()` supports only:
- path lookup: `a.b.c`, `a[0]`, `a[key]` (bracket key is itself resolved)
- literals: `true`, `false`, `null`, `undefined`, numbers, quoted strings
- unary `!`, parens, and top-level equality `===`, `!==`, `==`, `!=`
Anything else is a path miss (renders as a placeholder span). Text nodes may interpolate multiple `{{ }}`; attributes may be whole-expression (`value="{{ vmName }}"` → real value, preserving type: functions, React elements, booleans) or string-concatenated (`style="width:{{ navW }}px"`).

**Control tags**
- `<sc-for list="{{ arr }}" as="x" hint-placeholder-count="N">` — iterates; child scope gets `x` plus `$index`. Non-array ⇒ empty (warns). `hint-placeholder-count` is only used to draw N skeleton rows while the doc is still streaming.
- `<sc-if value="{{ b }}" hint-placeholder-val="{{ true|false }}">` — truthiness gate. `hint-placeholder-val` is the value assumed *only while streaming* so the initial route paints; it has no runtime meaning. In this design it marks the "default visible" branches: `isApp`, `isHome`, `vsOverview`, `essOpen`, `authStepEmail` are hinted `true`, everything else `false`.
- `<helmet>` → hoisted to document head (this design puts the global `<style>` there: fonts, colors, keyframes `pcspin`, `pcslide`, `pctoast`).

**Attributes**
- `onClick="{{ fn }}"` → mapped through `EVENT_MAP` to React `onClick`; the resolved value must be a **function** supplied by `renderVals()`. Same for `onChange`, `onKeyDown`, etc. Handlers receive the raw DOM event (design reads `e.target.value`, `e.key`).
- `style-hover="…css…"`, `style-focus="…"`, `style-active="…"` → the runtime mints a class in a generated stylesheet (`.scpN:hover{…!important}`) and appends it to `className`. Purely cosmetic.
- `class`→`className`, `for`→`htmlFor`, camelCase attrs are preserved via a `sc-camel-` encoding, `<select>/<table>/<tr>/<td>` are wrapped/unwrapped to survive HTML parsing.

**Logic class** — `data-dc-script` body is `new Function`-evaluated and must define `class Component extends DCLogic`. It has React-like `state`, `setState(patch|fn)`, `props`, `componentDidMount/DidUpdate/WillUnmount`. The single required method is **`renderVals()`**, which returns the flat object the template binds against (merged over `props`). `data-props` is a JSON prop-schema (editor metadata) — here:

| prop | editor | values | default |
|---|---|---|---|
| `operatorMode` | boolean | — | `false` |
| `startRoute` | enum | `home, landing, signin, catalog, wizard, resources, activity` | `home` |
| `deploySpeed` | range | 300–2500 ms, step 100 | `900` |

**Important architectural consequence:** everything the template sees is computed in `renderVals()` — including colors, SVG icons (React elements built in JS), and *pre-formatted display strings*. There is no formatting layer in the template.

---

## 2. STATE MODEL

### 2.1 `state` (initial, verbatim shape)

```js
{
  route: props.startRoute ?? 'home',   // 'home'|'landing'|'signin'|'signup'|'catalog'|'wizard'
                                       // |'deploy'|'vm'|'resources'|'activity'|'placeholder'
  navW: 220,                           // px; 220 expanded / 48 collapsed
  tenant: 'aurora-labs',
  project: 'All projects',             // 'All projects' | <project name>

  pane: null,                          // null|'tenant'|'notif'|'delete'|'json'  (right-hand flyouts)
  pal: false,                          // command palette open
  palQ: '', tenQ: '', catQ: '', vmFilter: '',   // search boxes

  toasts: [],                          // [{id:number, title, desc, kind:'ok'|'info'|'err'}]
  unread: 2,                           // notification badge count

  notifs: [
    { id:1, title:'Provisioning win-jump-01',
      desc:'Virtual machine deployment in progress in project sandbox.',
      time:'5 min ago', kind:'prog', prog:62 },
    { id:2, title:'Snapshot completed',
      desc:'apps-prod · etcd-backup finished successfully.',
      time:'2 h ago', kind:'ok' }
  ],

  resources: [ /* 10 seeded rows, see below */ ],
  vmd: { /* per-VM detail, keyed by name */ },
  wiz: { /* create-VM wizard, see below */ },
  deploy: null,                        // active deployment task or null

  vm: 'web-prod-01',                   // currently open resource (VM) name
  vmMenu: 'Overview',                  // resource-page left menu selection
  essOpen: true,                       // "Essentials" accordion
  delText: '',                         // typed-name delete confirmation buffer

  snaps: {},                           // { [vmName]: [{name, created, size}] }
  vmTags: { 'web-prod-01': [{k:'env',v:'prod'},{k:'owner',v:'alex'}] },
  vtK: '', vtV: '',                    // tag key/value inputs on VM Tags blade

  resType: 'all',                      // 'all'|'vm'|'k8s'|'db'|'net'|'storage'
  sel: {},                             // { [resourceName]: true }  row multi-select
  seed: 1,                             // chart RNG seed — bumped by Refresh
  ph: { title:'', desc:'' },           // "not built yet" placeholder page copy

  auth: { step:'email', email:'', pw:'', err:'', suName:'', suOrg:'' }
}
```

### 2.2 `resources[]` — the master resource list

Each entry: `{ name, type, t, project, status, tags[] }`

| name | type | t | project | status | tags |
|---|---|---|---|---|---|
| web-prod-01 | Virtual machine | vm | web-prod | Running | `['env: prod']` |
| gitlab-runner-02 | Virtual machine | vm | web-prod | Running | `['team: ci']` |
| pg-primary | Virtual machine | vm | data-staging | Stopped | `[]` |
| win-jump-01 | Virtual machine | vm | sandbox | Provisioning | `[]` |
| apps-prod | Kubernetes cluster | k8s | web-prod | Healthy | `['env: prod']` |
| dev-cluster | Kubernetes cluster | k8s | sandbox | Healthy | `[]` |
| orders-db | PostgreSQL 16 | pg | web-prod | Available | `['env: prod']` |
| cache-prod | Redis 7 | redis | web-prod | Available | `[]` |
| vnet-web-prod | Virtual network | net | web-prod | Active | `[]` |
| data-lake | S3 bucket | bucket | data-staging | Active | `[]` |

`t` is the icon/kind discriminator: `vm | k8s | pg | mongo | redis | net | lb | vol | bucket`. `tags` here are flattened display strings `"k: v"` (different from `vmTags`, which is structured).

### 2.3 `vmd` — VM detail records, keyed by VM name

```js
'web-prod-01': { node:'pve-node-02', size:'M (4 vCPU, 8 GB)', pub:'203.0.113.24',
                 priv:'10.10.1.4', img:'Ubuntu 24.04 LTS', created:'Mar 3, 2026 14:22' }
'gitlab-runner-02': { node:'pve-node-01', size:'L (8 vCPU, 16 GB)', pub:'—',
                 priv:'10.10.1.7', img:'Debian 12', created:'Jan 12, 2026 09:03' }
'pg-primary':  { node:'pve-node-03', size:'L (8 vCPU, 16 GB)', pub:'—',
                 priv:'10.20.2.5', img:'Ubuntu 24.04 LTS', created:'Nov 2, 2025 11:40' }
'win-jump-01': { node:'pve-node-02', size:'S (2 vCPU, 4 GB)', pub:'203.0.113.31',
                 priv:'10.30.0.9', img:'Windows Server 2022', created:'Today 08:12' }
```
`pub: '—'` is the sentinel for "no public IP". `size` is a *display string* `"<T-shirt> (<n> vCPU, <n> GB)"`; the Size blade parses it back with `.split(' ')[0]`.

### 2.4 `wiz` — create-VM wizard state

```js
{
  tab: 0,            // 0..6 current step
  maxTab: 0,         // highest unlocked step (tabs > maxTab are disabled)
  name: '',
  project: 'web-prod',
  image: 'Ubuntu 24.04 LTS',
  ssh: 'alex-workstation — ED25519',
  size: 'M',                                  // S|M|L|XL
  disks: [],                                  // [{name, type:'Data disk', size:<GB number>}]
  vnet: 'vnet-web-prod (10.10.0.0/16)',
  subnet: 'default (10.10.1.0/24)',
  pubIp: true,
  ports: { SSH:true, HTTP:false, HTTPS:false },
  cloud: '',                                  // cloud-init user-data (raw text)
  tags: [{ k:'env', v:'prod' }],
  tagK: '', tagV: ''                          // pending tag inputs
}
```

Select options hardcoded in the template (the real API must enumerate these):
- **project**: `web-prod`, `data-staging`, `sandbox`
- **image**: `Ubuntu 24.04 LTS`, `Debian 12`, `Windows Server 2022`
- **ssh**: `alex-workstation — ED25519`, `ci-deploy — RSA 4096`, `Generate new key pair`
- **vnet**: `vnet-web-prod (10.10.0.0/16)`, `vnet-staging (10.20.0.0/16)`
- **subnet**: `default (10.10.1.0/24)`, `backend (10.10.2.0/24)`
- **ports**: `SSH`=TCP 22, `HTTP`=TCP 80, `HTTPS`=TCP 443

### 2.5 `deploy` — the async provisioning task

```js
null | {
  name: 'web-prod-02',        // VM name; deployment id displayed as 'deploy-' + name
  project: 'web-prod',
  done: false,
  items: [                    // per-child-resource progress
    { label:'<name>',           type:'Virtual machine',    st:'Pending' },
    { label:'<name>-osdisk',    type:'Disk',               st:'Pending' },
    { label:'<name>-nic',       type:'Network interface',  st:'Pending' },
    { label:'<name>-ip',        type:'Public IP address',  st:'Pending' }  // only if wiz.pubIp
  ]
}
```
Item status enum: `Pending → Creating → Created`.

### 2.6 Class constants (not state, but backend-shaped reference data)

```js
SIZES = [ {name:'S',cpu:2,ram:4,price:18}, {name:'M',cpu:4,ram:8,price:36},
          {name:'L',cpu:8,ram:16,price:72}, {name:'XL',cpu:16,ram:32,price:144} ]   // EUR/month

TENANTS = [ {name:'aurora-labs',      domain:'aurora-labs.io',        projects:['web-prod','data-staging','sandbox']},
            {name:'helios GmbH',      domain:'helios-gmbh.example',   projects:['erp-prod','iot-staging']},
            {name:'platform-internal',domain:'ops.proxcloud.local',   projects:['bootstrap']} ]

CAT = [ {n,t,c,d} × 9 ]   // catalog: n=name, t=kind, c=category, d=description
// categories: Compute, Kubernetes, Databases, Networking, Storage
// kinds:      vm, k8s, pg, mongo, redis, net, lb, vol, bucket
```

### 2.7 Static data that is currently hardcoded inside `renderVals()` (i.e. must become API data)

`recent[]`, `quotas[]`, `costMonth`/`costSpark`, `healthItems[]`, `vmActivity[]`, `iamRows[]`, `nicRows[]`, `fwRows[]`, `vmDiskRows[]`, `vmCharts[]`, `vmMetrics[]`, `actRows[]`, `landSvcs[]`, `userName`/`userInitials`.

---

## 3. ACTIONS (every function reachable from the template)

### 3.1 Internal helpers (logic class)

| method | state effect | implied backend |
|---|---|---|
| `after(ms, fn)` | pushes `setTimeout` into `this.timers` | — (simulation of async) |
| `toast(title, desc, kind='ok')` | appends to `toasts`, auto-removes after **4200 ms** | client-only |
| `notify(n)` | prepends `{id, time:'Just now', ...n}` to `notifs`, `unread++` | server-pushed notification/task event |
| `go(route, extra)` | `{route, pane:null, pal:false, ...extra}` | client route change |
| `setWiz(patch)` | shallow-merges into `wiz` | — |
| `setStatus(name, status)` | patches one row of `resources` | reflects task/status poll |
| `statusColor(st)` | pure mapping (see §4.3) | — |
| `mi/fi/svc/spinner` | icon factories | — |
| `sparkEl(seed, h)` | deterministic 24-point LCG sparkline | metrics series |
| `wizErrors()` | pure validation over `wiz` + `resources` | server-side validation mirror |
| `estParts()` | pure price computation | pricing/quote endpoint |
| `depSet(i, st)` | patches `deploy.items[i].st` | task-item status |

### 3.2 Auth / landing

| binding | behavior | backend |
|---|---|---|
| `goLanding`, `goSignin`, `goSignup` | route changes; `goSignin` also resets `auth` to `{step:'email', err:'', pw:''}` | — |
| `onAuthEmail(e)` / `onAuthPw(e)` | `auth.email` / `auth.pw`, clears `err` | — |
| `authNext` | validates `/^[^@\s]+@[^@\s]+\.[^@\s]+$/`; on fail `err='Enter a valid email address, like alex@aurora-labs.io.'`; on pass `step='pw'` | `POST /auth/identify` (discover IdP / whether SSO) |
| `doSignIn` | requires non-empty pw (`err='Enter your password.'`), then `go('home')` + toast `Welcome back, Alex` | `POST /auth/login` → session/JWT |
| `authKeyNext` / `authKeySign` | Enter key → `authNext` / `doSignIn` | — |
| `authBack` | back to `step:'email'` | — |
| `forgotPw` | toast only | `POST /auth/password-reset` |
| `ssoClick` | toast only | OIDC redirect |
| `onSuName`, `onSuOrg` | `auth.suName`, `auth.suOrg` | — |
| `suSubmit` | `go('signin')`, toast `Request submitted` | `POST /tenant-requests {name, email, org}` |
| `signOut` | `go('landing')` + toast | `POST /auth/logout` |

### 3.3 Shell / chrome

| binding | behavior | backend |
|---|---|---|
| `toggleNav` | `navW: 220 ⇄ 48` | — |
| `goHome`, `goCatalog`, `goResources` (`{resType:'all', sel:{}}`), `goSettings` (placeholder), `goVms` (`{resType:'vm'}`) | routing | — |
| `helpClick` | toast | docs/support |
| `openPalette` | `{pal:true, palQ:''}` (also **Cmd/Ctrl+K** toggles) | — |
| `closePal`, `stopProp` | close / stop bubbling | — |
| `onPalQ` | `palQ` | client-side search over `resources` (top 5) |
| `p.go` (palette row) | quick actions or `openVm(name)` | search endpoint in real product |
| `openNotif` | `{pane:'notif', unread:0}` | mark-read |
| `dismissAll` | `{notifs:[], unread:0}` | dismiss all notifications |
| `openTenant` | `{pane:'tenant', tenQ:''}` | — |
| `closePane` | `{pane:null}` (also **Escape**) | — |
| `t.pick` (tenant row) | `{tenant, project:'All projects', route:'home'}` + toast | re-scope session to tenant |
| `p.pick` (project row) | `{project}` | scope filter |
| `onTenQ` | `tenQ` (filters tenants *and* projects) | — |
| `it.go` (navMain / navFavs / navBottom) | route + `resType` | — |

### 3.4 Home / catalog

| binding | behavior | backend |
|---|---|---|
| `t.go` (svcTiles) | VM tile → `resetWiz()` + `go('wizard')`; others → `go('catalog', {catCat})` | — |
| `r.go` (recent) | `openVm(name)` for `t==='vm'`, else resources list | — |
| `c.go` (catCats) | `{catCat}` | — |
| `onCatQ` | `catQ` | — |
| `c.create` (catItems) | `vm` → wizard; others → toast "Same pattern, later iteration" | per-kind create wizards |

### 3.5 Wizard

| binding | behavior | backend |
|---|---|---|
| `t.go` (wizTabs) | `setWiz({tab:i})`; button `disabled` when `i > maxTab` | — |
| `onVmName`, `onWizProject`, `onWizImage`, `onWizSsh` | field writes | — |
| `s.pick` (wizSizes) | `setWiz({size})` | — |
| `addDisk` | appends `{name:'<vmname|vm>-data-<n>', type:'Data disk', size:128}` | — |
| `d.del` | removes disk `i` | — |
| `onWizVnet`, `onWizSubnet` | field writes | — |
| `togglePubIp` | `pubIp = !pubIp` | — |
| `p.toggle` (portOpts) | toggles `ports[SSH|HTTP|HTTPS]` | firewall rule set |
| `onCloudInit` | `wiz.cloud` | cloud-init user-data |
| `onTagK`, `onTagV`, `addTag` (requires both non-empty), `t.del` | tag list edit | — |
| `goPrev` | `tab-1` (no-op at 0) | — |
| `goNext` | `tab+1`, `maxTab = max(maxTab, tab+1)` | — |
| `wizPrimary` | if `tab===6` → `createVm()`, else jump to `{tab:6, maxTab:6}` | — |
| **`createVm()`** | validation gate → builds `deploy` task, prepends new `resources` row (`status:'Provisioning'`), writes `vmd[name]`, `vmTags[name]`, routes to `deploy`, emits progress notification + staged timers | `POST /projects/{p}/vms` → returns task/UPID; children: disk, NIC, optional public IP |
| `resetWiz(name?)` | `{tab:0, maxTab:0, name:name||'', disks:[], cloud:'', tagK:'', tagV:''}` (keeps project/image/ssh/size/net/tags) | — |

### 3.6 Deployment page

| binding | behavior | backend |
|---|---|---|
| `goToResource` | `openVm(deploy.name)` | — |
| `createAnother` | `resetWiz()` + `go('wizard')` | — |

### 3.7 VM resource page

| binding | behavior | backend |
|---|---|---|
| `openVm(name)` | `go('vm', {vm:name, vmMenu:'Overview', vmFilter:'', essOpen:true})` | `GET /vms/{name}` |
| `onVmFilter` | `vmFilter` — filters the left blade menu (case-insensitive substring) | — |
| `m.go` (vmMenuGroups) | `{vmMenu: label}` | — |
| `toggleEss` | `essOpen` toggle (chevron rotates `0 ⇄ -90` deg) | — |
| `openJson` | `{pane:'json'}` | `GET /vms/{name}?view=json` (ARM-style) |
| `e.copy` | `navigator.clipboard.writeText(v)` + info toast `Copied to clipboard` | — |
| **cmd bar** `c.run`: | | |
| — `Connect` (disabled unless Running) | info toast "noVNC session … opens in a new tab" | `POST /vms/{id}/vncproxy` / websocket console |
| — `Start` (disabled unless Stopped) | `vmAction('Start')` | `POST /nodes/{node}/qemu/{vmid}/status/start` |
| — `Restart` (disabled unless Running) | `vmAction('Restart')` | `…/status/reboot` |
| — `Stop` (disabled unless Running) | `vmAction('Stop')` | `…/status/stop` (or `shutdown`) |
| — `Delete` (disabled while busy) | `{pane:'delete', delText:''}` | opens confirm flyout |
| — `Refresh` | `seed++` (re-rolls charts) | re-poll status + metrics |
| — `···` | info toast (resize / move project / clone) | — |
| **`vmAction(kind)`** | `setStatus(vm, {Start:'Starting', Stop:'Stopping', Restart:'Restarting'}[kind])`, then after **1800 ms** `setStatus(vm, {Start:'Running', Stop:'Stopped', Restart:'Running'}[kind])`, toast `"<kind> complete"`, notify `"<kind> virtual machine"` | task submit + poll to completion |
| **`confirmDelete()`** | `{pane:null, route:'home', delText:''}`, removes row from `resources`; info toast `Deleting <vm>`; after **2400 ms** notify + toast `Delete complete` | `DELETE /vms/{id}` (cascades disks, NIC, public IP) |
| `onDelText` | `delText`; `delDisabled = delText !== vm` | — |
| `addVmTag` (needs `vtK && vtV`), `onVtK`, `onVtV`, `t.del` (vmTagRows) | mutate `vmTags[vm]` | `PUT /vms/{id}/tags` |
| `takeSnap` | prepends `{name:'<vm>-snap-<n+1>', created:'Just now', size:'64 GB'}` to `snaps[vm]`, info toast, after **2000 ms** notify `Snapshot completed` | `POST /vms/{id}/snapshot` |
| `s.pick` (vmSizeCards) | rewrites `vmd[vm].size`, `setStatus(vm,'Resizing')`, after **2000 ms** → `'Running'` + toast `Resize complete` | `PUT /vms/{id}/config {cores,memory}` + restart |

### 3.8 Resources list & activity

| binding | behavior | backend |
|---|---|---|
| `resRefresh` | `seed++` | re-list |
| `r.toggleSel` | toggles `sel[name]` | — |
| `bulkClear` | `sel = {}` | — |
| `bulkStart` / `bulkStop` | info toast listing `selNames`, then clears `sel` (**no real state change in the design**) | batch start/stop |
| `bulkDelete` | error toast: "Destructive bulk actions require type-to-confirm per resource" | intentionally blocked |
| `p.click` (resPills) | pill 1 → `{pane:'tenant'}`; pill 2 → `{resType:'all', sel:{}}`; pill 3 → info toast | filter UI |
| `r.go` (resRows) | VM → `openVm`; others → info toast "Same anatomy" | per-kind detail pages |
| `a.go` (actRows) | navigates to referenced resource if it is a VM, else resources list | audit-log → resource deep link |

---

## 4. DERIVED DATA CONTRACT

### 4.1 Global / session

| binding | type | example | note |
|---|---|---|---|
| `userName`, `userInitials` | string | `Alex Meyer`, `AM` | hardcoded → session endpoint |
| `tenantName` | string | `aurora-labs` | |
| `projectLabel` | string | `All projects` | sentinel for "no project filter" |
| `greeting` | string | `Good morning, Alex` | computed client-side from `new Date().getHours()` (<12 / <18 / else) |
| `notifCount` / `hasNotifBadge` | number / bool | `2` / `true` | |
| `isOperator` | bool | from prop `operatorMode` | renders gold "OPERATOR" badge + 3px `#FFB900` stripe |
| `navW` | number (px) | `220` \| `48` | |

### 4.2 Tenants / projects

`tenRows[]`: `{ name, domain, active, bg, dotBd, pick }` — `domain` is a display-only string.
`projRows[]`: `{ name, active, bg, dotBd, pick }` — first entry is the literal `'All projects'`.

### 4.3 Status vocabulary (exact strings — this is the critical contract)

`statusColor()` mapping:

| color | hex | statuses |
|---|---|---|
| green | `#107C10` | `Running`, `Healthy`, `Available`, `Active`, `Succeeded`, `Created`, `Attached` |
| gray | `#605E5C` | `Stopped`, `Pending` |
| red | `#D13438` | `Failed`, `Deny` |
| blue (default/transient) | `#0078D4` | everything else → `Provisioning`, `Creating`, `Starting`, `Stopping`, `Restarting`, `Resizing`, `In progress` |

Additional literals: `Deleted` (fallback `vmStatus` when the resource is gone), `Allow` (firewall action).

Status by domain:
- **Resource (VM)**: `Running | Stopped | Provisioning | Starting | Stopping | Restarting | Resizing | Deleted`
- **Kubernetes**: `Healthy`; **Database**: `Available`; **Network/Bucket**: `Active`
- **Deployment item**: `Pending | Creating | Created`
- **Activity/audit**: `Succeeded | Failed | In progress`
- **Disk state**: `Attached`

Derived booleans on the VM page: `running = status==='Running'`, `stopped = status==='Stopped'`, `busy = !running && !stopped` (drives command-bar disabling).

### 4.4 Resource list (`resRows[]`)

Backend must supply: `name`, `type` (human label, e.g. `PostgreSQL 16`), `t` (kind key), `project`, `status`, `tags: string[]` in `"key: value"` form. UI adds `icon`, `statusColor`, `sel`, `rowBg`, `boxBd`, `boxBg`, `toggleSel`, `go`.
Table columns: **Name | Type | Project | Status | Tags** (+ checkbox).
Type filter mapping (`resType` → predicate on `t`): `all`=∗, `vm`, `k8s`, `db`∈{pg,mongo,redis}, `net`∈{net,lb}, `storage`∈{vol,bucket}.
Titles: `resTitles = {all:'All resources', vm:'Virtual machines', k8s:'Kubernetes clusters', db:'Databases', net:'Networking', storage:'Storage'}`.
Project filter is applied *after* type filter: `S.project === 'All projects' || r.project === S.project`.

### 4.5 Home

- `recent[]`: `{ name, t, type, project, when }` — `when` is a **humanized string** (`'2 h ago'`, `'5 h ago'`, `'Yesterday'`, `'2 d ago'`, `'3 d ago'`). Filtered against existing `resources`. Column header: "Last viewed".
- `quotas[]`: `{ label, used, limit, pct }` — **`used`/`limit` are pre-formatted strings with units baked in** (`'32'/'48'`, `'96 GB'/'128 GB'`, `'1.2 TB'/'2 TB'`); `pct` is an **integer 0–100** used directly as `width:{{ q.pct }}%`.
- `costMonth`: string with currency symbol, `'€412.38'`. `costSpark`: sparkline element, **fixed seed 7** (never changes on refresh).
- `healthItems[]`: `{ label }` only — the green dot is hardcoded, so there is currently *no* health status field. A real API needs `{label, status}`.

### 4.6 VM detail

`essL[]` (left column) — `{k, v}`: `Status`, `Project`, `Node`, `Size`.
`essR[]` (right column) — `{k, v, cp, copy}`: `Public IP` (copyable only when present and `!== '—'`), `Private IP` (always copyable), `OS image`, `Created`.

Field mapping to `vmd`: `node`, `size`, `pub`, `priv`, `img`, `created`.

`vmJson` (JSON-view flyout) is the canonical resource document:
```json
{ "name", "type": "proxcloud/virtual-machine", "tenant", "project", "status",
  "node", "size", "publicIp", "privateIp", "image", "created",
  "tags": { "<k>": "<v>" } }
```

Other blades:
- `vmActivity[]`: `{ action, actor, when, status, color }` — `actor` is an email or service-account name (`ci-bot`).
- `iamRows[]`: `{ name, type, role, scope }` — `type ∈ {User, Service account}`, `role ∈ {Owner, Contributor, Reader}`, `scope` strings `'Tenant (inherited)'`, `'Project: web-prod'`. Columns: Name | Type | Role | Scope.
- `nicRows[]`: `{k,v}` — `Network interface` (`<vm>-nic`), `Virtual network`, `Subnet`, `Private IP`, `Public IP`.
- `fwRows[]`: `{ prio, name, port, proto, src, action, actionColor }` — e.g. `100/allow-ssh/22/TCP/Any/Allow`, `65000/deny-all-inbound/Any/Any/Any/Deny`. Columns: Priority | Name | Port | Protocol | Source | Action.
- `vmDiskRows[]`: `{ name, type, size, state }` — `size` is a **string with unit** (`'64 GB'`), `type ∈ {OS disk, Data disk}`, `state:'Attached'`.
- `snapRows[]`: `{ name, created, size }` — `created` humanized (`'Just now'`), `size` string.
- `vmTagRows[]`: `{ k, v, del }` (structured key/value, unlike `resources[].tags`).

### 4.7 Charts / metrics

The design **fakes** all series. `sparkEl(seed, height)`:
- deterministic LCG (`x = (x*9301+49297) % 233280`), **24 points**, value clamped to `[6, 92]`, random walk step ±12
- rendered into `viewBox="0 0 100 34"`, `preserveAspectRatio="none"`, area fill `#DEECF9`, line `#0078D4`, two gridlines
- the third parameter `pct` is declared but unused

Cards:

| binding | entries | height |
|---|---|---|
| `vmCharts` (Overview) | `CPU (average)` = `'12%'` (seed+3), `Memory used` = `'58%'` (seed+11), `Network (in/out)` = `'3.2 MB/s'` (seed+27) | 48 |
| `vmMetrics` (Metrics blade) | the above three plus `Disk IOPS` = `'340'` (seed+19) | 90 |

Shape per card: `{ label, value, el }` where **`value` is a pre-formatted string** and `el` is the rendered SVG. Metrics blade subtitle declares the intended window: **"Last hour · 1-minute granularity"** (⇒ 60 points, 1 point/min).

**Real contract needed:** `{ metric, unit, points: [{ t: <ISO8601 | epoch ms>, v: <number> }] }` with a formatted `value` (or unit + current value). Percent semantics in the design are **0–100 with a `%` suffix in the string**, not 0–1. Network is bytes/s already formatted to `MB/s`.

### 4.8 Pricing / estimate

`estRows[]`: `{ label, value }`; `estTotal`: string.
- Compute: `SIZES[size].price` €/month (S 18, M 36, L 72, XL 144)
- OS disk: fixed **€5.12** (= 64 GB × €0.08)
- Data disks: `sum(disks.size) × €0.08` /GB/month
- Public IP: **€3.00**/month
- All rendered as `'€' + n.toFixed(2)`, currency **EUR hardcoded**.
`estNote` / `estNoteColor`: `'Configuration is valid'` + `#107C10`, or `'<n> issue(s) to resolve before create'` + `#D13438`.

### 4.9 Time formats

There is **no** `formatBytes`, **no** uptime formatter, **no** date library anywhere. Every temporal value is a display string produced upstream:
- relative: `'Just now'`, `'5 min ago'`, `'2 h ago'`, `'Yesterday'`, `'2 d ago'`, `'3 d ago'`, `'Last week'`
- semi-absolute: `'Today 08:12'`, `'Yesterday 22:10'`, `'Mon 16:31'`
- absolute: `'Mar 3, 2026 14:22'`, `'Jan 12, 2026 09:03'`, `'Nov 2, 2025 11:40'`

**Recommendation for the real backend:** return ISO-8601 timestamps and byte/percent numbers, and add a client formatting layer — the design's strings are the *target output* of that layer, not the wire format.

### 4.10 Notifications / toasts

`notifRows[]`: `{ id, title, desc, time, kind, prog?, icon, hasProg }`
- `kind ∈ 'prog' | 'ok' | 'err'`; icon: spinner / green `checkC` / red `warn`
- `hasProg = kind==='prog' && prog !== undefined`; `prog` is **0–100 integer**, bar width `{{ n.prog }}%` with `transition:width .4s`

`toasts[]`: `{ id, title, desc, kind, accent, icon }`
- `kind ∈ 'ok' | 'info' | 'err'` → accents `#107C10` / `#0078D4` / `#D13438`
- auto-dismiss 4200 ms, `animation: pctoast .2s`, stacked top-right under the header (`top:48px; right:12px`)

---

## 5. INTERACTION FLOWS

### 5.1 Create-resource wizard (VM)

**Entry points** → all call `resetWiz()` then `go('wizard')`: home "Virtual machine" service tile, catalog card `Create` for the VM kind, command-palette "Create a virtual machine", deployment page "Create another VM". Non-VM kinds emit an info toast instead ("The `<name>` create flow reuses the VM wizard frame").

**Tabs** (`tabNames`): index 0 `Basics`, 1 `Size`, 2 `Disks`, 3 `Networking`, 4 `Advanced`, 5 `Tags`, 6 `Review + create`.
- Gating: tab button is `disabled` when `i > wiz.maxTab`; locked tabs render `#A19F9D`, active tab `#323130` + weight 600 + 2px `#0078D4` underline.
- `goNext` advances and unlocks (`maxTab = max(maxTab, tab+1)`); `goPrev` steps back, disabled at 0.
- The primary button is **`Review + create`** for tabs 0–5 (jumps straight to tab 6 and unlocks it, `maxTab:6`) and **`Create`** on tab 6. Secondary: `< Previous`, `Next : <nextLabel> >` (hidden on tab 6).

**Validation** (`wizErrors()`, evaluated on every render):
1. empty name → `'Virtual machine name is required (Basics).'`
2. `!/^[a-z][a-z0-9-]{0,39}$/.test(name)` → `'VM name must start with a lowercase letter and contain only lowercase letters, numbers, and hyphens (Basics).'`
3. name collides with an existing `resources[].name` → `'A resource named "<name>" already exists in this tenant (Basics).'`
Inline display on Basics: `hasNameErr = !!name && errs.length > 0`, message with the `' (Basics).'` suffix stripped, input border switches `#8A8886 → #D13438`. Review tab shows either a green "Validation passed" banner (`valOk`) or the red `valErrors` list (`hasErrors`). The sticky cost card always shows `estNote`.

**Submit (`createVm`)**
1. If `wizErrors().length` → force `{tab:6, maxTab:6}` and **abort** (no partial creation).
2. Build `items[]` = VM, `-osdisk`, `-nic`, and `-ip` when `pubIp` — all `st:'Pending'`.
3. Single atomic `setState`: `route:'deploy'`, `deploy:{name, project, items, done:false}`, prepend the new resource with `status:'Provisioning'` and `tags` flattened to `"k: v"`, write `vmd[name] = {node:'pve-node-01', size:'<S> (<cpu> vCPU, <ram> GB)', pub: pubIp?'203.0.113.42':'—', priv:'10.10.1.12', img, created:'Just now'}`, copy `wiz.tags` into `vmTags[name]`.
4. `notify({title:'Deployment started', desc:'Creating virtual machine <name> in project <p>.', kind:'prog', prog:5})`.
5. Per item `i` with `sp = props.deploySpeed ?? 900`:
   - at `400 + i*sp` → `Pending → Creating`
   - at `400 + i*sp + round(sp*1.4)` → `Creating → Created`, and the `'Deployment started'` notification's `prog` is set to `round((i+1)/items.length*100)`
   - on the **last** item: `deploy.done = true`; the notification is rewritten in place to `{title:'Deployment succeeded', desc:'<name> is running in project <p>.', kind:'ok', prog: undefined}` and `unread++`; `setStatus(name,'Running')`; toast `Deployment succeeded`.

**Deployment page** shows `depIcon` (spinner → green check), `depTitle` (`Deployment is in progress` → `Your deployment is complete`), `depSub` = `Deployment name: deploy-<name> · Tenant: <tenant> · Project: <project>`, and a **Resource | Type | Status** table with per-row icon (clock/spinner/check). When `depDone`: `Go to resource` and `Create another VM` buttons appear. Standing copy: *"You can safely leave this page — progress stays available in the notification bell."* → the backend needs a durable task record + notification feed, not just a request/response.

> Backend implication: one create call must return a **task with child sub-tasks**, each with its own status, plus an overall progress percent. Proxmox UPID-per-step maps naturally: qemu create → disk alloc → net attach → IP alloc.

### 5.2 Delete with typed-name confirmation

1. Command bar `Delete` (disabled while `busy`) → `{pane:'delete', delText:''}` opens a 400px right flyout.
2. Flyout shows a red warning box "Deleting **`<vm>`** is permanent and cannot be undone." plus an explicit cascade list: *All attached disks and their snapshots · The network interface and its private IP reservation · Any public IP assigned to this VM*.
3. Input with `placeholder = vmName2`; `delDisabled = delText !== vm`; the Delete button background is `#F3F2F1` (inert) until exact match, then `#D13438`.
4. `confirmDelete()`: closes the pane, routes to `home`, removes the row from `resources`, clears `delText`; info toast `Deleting <vm>`; after **2400 ms** → notify `Deleted virtual machine` + toast `Delete complete`.
5. Bulk delete is deliberately refused: `bulkDelete` shows an `err` toast — "Destructive bulk actions require type-to-confirm per resource."

### 5.3 Start / Stop / Restart with pending states

- Availability: `Connect` needs `Running`; `Start` needs `Stopped`; `Restart`/`Stop` need `Running`; `Delete` blocked while `busy`. Disabled buttons render `#A19F9D` and pass `disabled={{ c.disabled }}`.
- `vmAction(kind)` immediately writes the transitional status (`Starting`/`Stopping`/`Restarting`, all rendered blue), then after **1800 ms** writes the terminal status (`Running`/`Stopped`/`Running`), fires a toast `"<kind> complete — <vm> is now <status>."` and a notification `"<kind> virtual machine"` / `"<vm> · completed successfully."`.
- The status pill in the page header and every `resources` row update from the same `resources[].status` field, so a single status source drives all views.
- Resize follows the same shape: optimistic `vmd[vm].size` write → `Resizing` → 2000 ms → `Running` + toast.
- Snapshot: optimistic row insert → info toast → 2000 ms → completion notification.

> Backend implication: every lifecycle action is async and needs (a) an immediate transitional status the UI can show, and (b) a completion event feeding both toast and notification center.

### 5.4 "Live" charts

There is **no `setInterval` and no animation of series data** — the only chart motion in the design is:
- CSS `@keyframes pcspin` (spinners), `pcslide` (flyout entry, .18s), `pctoast` (toast entry, .2s), and `transition:width .4s` on notification progress bars.
- `state.seed` is incremented by the VM command-bar **Refresh** and the resources-list **Refresh**; `vmCharts`/`vmMetrics` derive from `seed + 3 / +11 / +19 / +27`, so a refresh re-rolls a completely new deterministic series. `costSpark` uses the constant seed `7` and therefore never changes.
- The displayed values (`'12%'`, `'58%'`, `'340'`, `'3.2 MB/s'`) are **static strings** and do not track the drawn series.

For the real product this becomes: poll (or SSE/WebSocket) an RRD-style endpoint on the declared cadence — "Last hour · 1-minute granularity" — returning ~60 `{t, v}` points per metric plus a current value.

### 5.5 Search / filter / sort

- **Command palette** — `Cmd/Ctrl+K` toggles (`pal`, resets `palQ`), `Escape` closes palette *and* any flyout. Rows = `palQuick` (3 static quick actions, filtered by substring on their label) **+** `palRes` (resources whose `name` contains the query, **max 5**, only when the query is non-empty), each `{icon, label, hint, go}` with `hint` = `'<type> · <project>'`. Backdrop click closes; inner panel calls `stopProp`.
- **Catalog** — `catCat` (category chip) AND `catQ` (case-insensitive substring on catalog item name).
- **Resource list** — two-stage: `resType` predicate over `t`, then `project` equality (skipped for `'All projects'`). Rendered as removable filter pills. Sorting is **not implemented** anywhere (no sort state, no clickable headers).
- **VM blade menu** — `vmFilter` filters menu item labels; a group title only renders when at least one of its items survives (`g.hasTitle`).
- **Tenant flyout** — `tenQ` filters tenants *and* the project list simultaneously.
- Multi-select: `sel[name]` map, `selCount`, and a blue action bar (`Start | Stop | Delete | Clear selection`) that appears when `hasSel`.

### 5.6 Nav collapse

`toggleNav` flips `navW` between `220` and `48`. The rail uses `transition:width .15s ease` and `overflow-x:hidden`; each item is a 48px fixed icon slot plus a label that clips away at 48px. Every nav button also carries `title="{{ it.label }}"` so it stays usable when collapsed. Active state is `bg:#DEECF9` computed by `navBg(route === …)` (favorites additionally match on `resType`).

### 5.7 Toasts & notification center

- `toast(title, desc, kind)` pushes `{id: Date.now()+Math.random(), title, desc, kind}` and schedules removal at **4200 ms**. Container is `position:fixed; top:48px; right:12px; z-index:70; pointer-events:none` with each card `pointer-events:auto` — non-blocking by design. Left border 3px in the kind accent.
- Toasts are **ephemeral**; anything durable is mirrored into `notifs` via `notify()`. The deployment flow is the only place that *mutates an existing notification* (matched by `title === 'Deployment started'`) to advance `prog` and then flip it to a success notification — i.e. the notification is the task's UI representation and needs a stable id in the real API (the design matching on title is a prototype shortcut).
- Bell badge shows `unread` (`hasNotifBadge = unread > 0`); opening the pane zeroes it. `Dismiss all` clears both `notifs` and `unread`. Empty state: "No new notifications."

### 5.8 Auth flow

`landing → signin (step 'email' → step 'pw') → home`. Email step validates format locally; password step only checks non-emptiness; success routes home with a welcome toast. `signup` ("Request a tenant") collects `suName`, `authEmail`, `suOrg` and returns to sign-in with a "Request submitted" toast — i.e. **tenant creation is operator-mediated, not self-service**. `isApp = !['landing','signin','signup'].includes(route)` gates the entire portal chrome.

---

## Gaps worth flagging before API design

1. **No sort, no pagination, no server-side search** anywhere — the list contract is currently "return everything for the tenant".
2. **`resources[].tags` vs `vmTags[]`** are two different shapes for the same concept (`"k: v"` strings vs `{k,v}` objects). Pick one wire format.
3. **`size` is a lossy display string** (`'M (4 vCPU, 8 GB)'`) that the Size blade re-parses with `.split(' ')[0]`. The API should return `{sizeName, cpu, ram}` separately.
4. **Node placement is faked** — `createVm` always writes `node:'pve-node-01'`; the real create needs a scheduler/node-selection input or response.
5. **IPs are faked constants** (`203.0.113.42`, `10.10.1.12`) — allocation must come back on the task.
6. **`healthItems[]` carries no status field**; **`quotas[]` has no raw numbers**; **metric card `value` is decoupled from the series**. These three need real numeric contracts.
7. **Notification identity** is matched by title string during the deploy flow — needs a real task/notification id.
8. Non-VM resource kinds have **no detail page and no create flow** (they emit "later iteration" toasts), but the catalog, list, and icon system already model all nine kinds `vm, k8s, pg, mongo, redis, net, lb, vol, bucket`.
