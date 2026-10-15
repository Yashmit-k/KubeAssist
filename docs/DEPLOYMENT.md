# Deploying KubeAssist

There are two shapes of deployment:

- **Single operator** — the backend diagnoses one cluster it runs next to (local mode). Simplest; run it inside that cluster or anywhere with a kubeconfig.
- **Multi-tenant SaaS** — `ENABLE_LOCAL_MODE=false`, `ENABLE_RELAY=true`. Users register clusters in the UI and install the agent; nothing of the operator's own cluster is exposed.

## Components

| Component | Image (from `release.yml`) | Suggested host |
| --- | --- | --- |
| Go backend | `ghcr.io/<owner>/kubeassist-backend` | Fly.io, Render, Railway, or a VPS with Caddy |
| AI service | `ghcr.io/<owner>/kubeassist-ai` | Same platform, private network if possible |
| Frontend | `ghcr.io/<owner>/kubeassist-frontend` (nginx, proxies `/api`), or `frontend/dist` on Vercel / Netlify / Cloudflare Pages | |
| Postgres | — | Neon, Supabase, Render |
| Agent | `ghcr.io/<owner>/kubeassist-agent` (amd64 + arm64) | Customers' clusters |

Publish images by pushing a tag: `git tag v1.0.0 && git push origin v1.0.0`.
GHCR packages start **private** — make `kubeassist-agent` public (GitHub → Packages → Settings) so customers can pull it.

## Multi-tenant checklist

Backend environment:

```bash
ENABLE_LOCAL_MODE=false
ENABLE_RELAY=true
RELAY_PUBLIC_URL=wss://api.example.com/agent/connect   # must be wss:// — the agent token travels in it
DATABASE_URL=postgres://...?sslmode=require            # registrations + history survive restarts
AI_BASE_URL=http://kubeassist-ai.internal:8000
AI_AUTH_TOKEN=<long random secret>                     # same value as AI_SHARED_TOKEN on the AI service
AGENT_IMAGE=ghcr.io/<owner>/kubeassist-agent:v1.0.0    # pin a version in generated manifests
CORS_ALLOWED_ORIGINS=https://app.example.com           # only if the UI is on a different origin
ADMIN_TOKEN=<optional, for operators>
```

AI service environment: `AI_SHARED_TOKEN`, `LLM_MOCK=false`, `LLM_API_KEY`, `LLM_BASE_URL`, `LLM_MODEL`.

TLS: terminate at the platform's load balancer or with Caddy (`reverse_proxy backend:8080` handles WebSocket upgrades and SSE without extra config). If you use nginx, disable buffering for `/api/` and pass `Upgrade` headers for `/agent/` — see [`frontend/nginx.conf.template`](../frontend/nginx.conf.template).

Frontend hosted separately: build with `VITE_API_BASE_URL=https://api.example.com` and list that UI origin in `CORS_ALLOWED_ORIGINS`.

## Known limitations before opening to strangers

- No user accounts: anyone can register a cluster ID; access is controlled solely by each cluster's token (held in the user's browser). Add authentication before treating this as a multi-user product.
- No rate limiting on `POST /api/clusters` or troubleshoot; put it behind your platform's rate limiter.
- Agent images are not signed; consider cosign signing in `release.yml` and publishing digests.

## Single operator, in-cluster

Apply [`deploy/rbac/kubeassist-readonly.yaml`](../deploy/rbac/kubeassist-readonly.yaml), run the backend image with `serviceAccountName: kubeassist-backend`, leave `KUBECONFIG` unset (in-cluster credentials) and `ENABLE_LOCAL_MODE=true`. Do not expose it publicly without authentication in front of it — local mode has no login.
