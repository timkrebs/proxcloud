<!-- Keep the PR small and one logical change. Conventional commit title (feat:/fix:/refactor:/test:/docs:/chore:). -->

## What & why

<!-- One or two sentences: what this changes and the reason. Link the issue/ADR if any. -->

## Definition of done (CLAUDE.md)

- [ ] Works against a real Proxmox server, or the mocked client in tests — but the wiring is real. No mock/fabricated data.
- [ ] Loading skeleton, empty state, and error state implemented (for UI changes).
- [ ] Backend handlers have table-driven tests with the mocked Proxmox client.
- [ ] No secrets in code, logs, or images. `PROXMOX_TOKEN_SECRET` / `SECRETS_KEY` stay server-side.
- [ ] Matches the design tokens/layout (no hardcoded colors or spacing).
- [ ] README / docs / ADR updated if setup or API surface changed.

## Tenancy iron rules (if this touches tenant-scoped code)

- [ ] Every tenant-scoped query is tenant-filtered in the query itself.
- [ ] Every mounted route has an `internal/authz` permission-table entry (completeness test passes).
- [ ] Every mutation records an audit entry at the choke-point.
- [ ] Cross-tenant access returns 404, never 403.

## Database migration

- [ ] No migration in this PR, **or**:
- [ ] The migration is **backward-compatible** with the currently-deployed code (expand/contract): the old app keeps running against the new schema, and the new app runs against the pre-migration schema for the blue/green window.
- [ ] Rollback impact considered (a proxy switch-back must not require a schema downgrade).

## Verification

<!-- How you tested: `go test ./...`, `npm run test/build`, live against pve01, smoke, etc. -->
