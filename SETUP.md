# Proxcloud — .claude/ agent setup

Drop this whole directory structure into the root of your Proxcloud repo (merge with what's there).

```
proxcloud/
├── CLAUDE.md                      # Shared project memory — iron rules, architecture, DoD
├── .claude/
│   ├── settings.json              # Pre-approved safe commands, .env read-protection
│   ├── agents/
│   │   ├── architect.md           # Plans, ADRs, API contracts (opus, docs-only writes)
│   │   ├── backend-engineer.md    # Go backend + Proxmox client
│   │   ├── frontend-engineer.md   # Next.js UI from the Claude Design import
│   │   ├── proxmox-specialist.md  # PVE API endpoints, privileges, gotchas (web-verified)
│   │   ├── qa-engineer.md         # Tests, DoD enforcement, mock-data hunter
│   │   ├── security-reviewer.md   # Read-only security gate (opus)
│   │   ├── devops-engineer.md     # Docker, compose, CI, deployment docs
│   │   └── code-reviewer.md       # Read-only quality gate (sonnet)
│   └── commands/
│       ├── build-feature.md       # /build-feature <desc> — full plan→build→test→review loop
│       ├── verify-proxmox.md      # /verify-proxmox — live connectivity + privilege check
│       ├── review.md              # /review — run the full review gate on the current diff
│       └── adr.md                 # /adr <topic> — record an architecture decision
```

## How to drive it

1. Start with the main Proxcloud build prompt (design import + requirements).
2. First command: `/verify-proxmox` — make sure the token and privileges work before any UI exists.
3. Then per feature: `/build-feature the resource dashboard with live node stats`, `/build-feature the create-resource wizard for LXC`, etc.
4. Before every commit: `/review`.

## Notes

- Subagents are loaded at session start — restart the session after editing these files.
- `settings.json` denies reading `.env` so the token secret never enters any agent's context; agents read `.env.example` for the variable names instead.
- Model choices: architect + security-reviewer use opus (judgment-heavy, low volume), code-reviewer uses sonnet, engineers inherit your session model. Adjust the `model:` field per file if you want a different cost profile.
