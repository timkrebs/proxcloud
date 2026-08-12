# syntax=docker/dockerfile:1

# ── dev: next dev with the source bind-mounted by compose (`target: dev`) ────
FROM node:22-slim AS dev
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci
COPY . .
EXPOSE 3000
CMD ["npm", "run", "dev"]

# ── deps: install once, cached on the lockfile ───────────────────────────────
FROM node:22-slim AS deps
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci

# ── build: Next standalone output (.next/standalone + .next/static) ──────────
FROM node:22-slim AS build
WORKDIR /app
ENV NEXT_TELEMETRY_DISABLED=1
# BACKEND_ORIGIN is baked into the /api rewrite at build time (Next evaluates
# next.config rewrites during `next build`). Behind the ADR-0015 reverse proxy
# this is usually unused, but release-engineer can pin it here if the frontend
# must reach the backend directly.
ARG BACKEND_ORIGIN
ENV BACKEND_ORIGIN=${BACKEND_ORIGIN}
COPY --from=deps /app/node_modules ./node_modules
COPY . .
RUN npm run build

# ── prod: slim node runner, non-root, standalone server ──────────────────────
FROM node:22-slim AS prod
WORKDIR /app
ARG SHA=dev
ARG SEMVER=0.0.0-dev
ARG BUILDTIME=unknown
LABEL org.opencontainers.image.source="https://github.com/timkrebs/proxcloud" \
      org.opencontainers.image.revision="${SHA}" \
      org.opencontainers.image.version="${SEMVER}" \
      org.opencontainers.image.created="${BUILDTIME}" \
      org.opencontainers.image.title="proxcloud-frontend" \
      org.opencontainers.image.description="Proxcloud Next.js portal — never talks to Proxmox directly."

ENV NODE_ENV=production \
    NEXT_TELEMETRY_DISABLED=1 \
    PORT=3000 \
    HOSTNAME=0.0.0.0

# Next's standalone contract: the traced server + minimal node_modules land in
# .next/standalone; static assets and public/ are copied alongside by hand.
COPY --from=build /app/.next/standalone ./
COPY --from=build /app/.next/static ./.next/static
COPY --from=build /app/public ./public

EXPOSE 3000

# node:slim ships no wget/curl; probe with node's http client. Any status < 500
# means the server is up and routing (the root path may 3xx to /login).
HEALTHCHECK --interval=10s --timeout=3s --start-period=15s --retries=6 \
  CMD ["node", "-e", "require('http').get('http://127.0.0.1:3000/',r=>process.exit(r.statusCode<500?0:1)).on('error',()=>process.exit(1))"]

# node:22-slim provides an unprivileged `node` user (UID 1000).
USER node
CMD ["node", "server.js"]
