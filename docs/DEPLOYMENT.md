# Deploying KubeAssist

There are two shapes of deployment:

- **Single operator** — the backend diagnoses one cluster it runs next to (local mode). Simplest; run it inside that cluster or anywhere with a kubeconfig.
- **Multi-tenant SaaS** — `ENABLE_LOCAL_MODE=false`, `ENABLE_RELAY=true`. Users sign in (GitHub), register clusters in the UI and install the agent; each cluster is visible only to its owner, and nothing of the operator's own cluster is exposed.

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

# Accounts
PUBLIC_URL=https://app.example.com                     # the UI's origin; https => Secure __Host- cookies
GITHUB_CLIENT_ID=<GitHub OAuth App client ID>
GITHUB_CLIENT_SECRET=<GitHub OAuth App client secret>
# AUTH_DEV_LOGIN must stay unset (it is refused with an https PUBLIC_URL anyway)

# Abuse limits (defaults shown; "off" disables)
MAX_CLUSTERS_PER_USER=10
RATE_LIMIT_REGISTER=10/h                               # per user
RATE_LIMIT_TROUBLESHOOT=30/m                           # per user (per IP when anonymous)
RATE_LIMIT_LOGIN=30/m                                  # per IP
TRUST_PROXY_HEADERS=true                               # only if your proxy sets X-Forwarded-For
```

GitHub sign-in: create an OAuth App (GitHub → Settings → Developer settings →
OAuth Apps) with **Homepage URL** `https://app.example.com` and **Authorization
callback URL** `https://app.example.com/api/auth/github/callback`. No scopes are
requested; KubeAssist reads only the public profile.

The UI and API must be **same-site** (the same origin, or subdomains of one
registrable domain such as `app.example.com` + `api.example.com`): the session
cookie is `SameSite=Lax`, so a UI on an unrelated domain can't send it. Serving
both from one origin (the nginx frontend image proxies `/api`) is simplest and
needs no CORS at all. Users, sessions and cluster ownership live in Postgres;
without `DATABASE_URL` they are kept in memory and lost on restart.

Rate limits are per backend process. With several replicas each enforces its own
budget; put a shared limiter at the edge if you need strict global limits.

AI service environment: `AI_SHARED_TOKEN`, `LLM_MOCK=false`, `LLM_API_KEY`, `LLM_BASE_URL`, `LLM_MODEL`.

TLS: terminate at the platform's load balancer or with Caddy (`reverse_proxy backend:8080` handles WebSocket upgrades and SSE without extra config). If you use nginx, disable buffering for `/api/` and pass `Upgrade` headers for `/agent/` — see [`frontend/nginx.conf.template`](../frontend/nginx.conf.template).

Frontend hosted separately: build with `VITE_API_BASE_URL=https://api.example.com`, list that UI origin in `CORS_ALLOWED_ORIGINS` (it then also gets `Access-Control-Allow-Credentials`), keep it same-site with the API, and set `PUBLIC_URL` to the origin that serves `/api/auth/github/callback`.

Upgrading from a version without accounts: existing registrations have no owner
and are reachable only with `ADMIN_TOKEN`. Revoke them
(`DELETE /api/clusters/{id}` with the admin token) and let users re-register.

## Known limitations

- GitHub is the only production sign-in provider (no email magic link yet), and there is no account deletion or team sharing — a cluster has exactly one owner.
- Rate limits are in-process (see above).
- Agent images are not signed; consider cosign signing in `release.yml` and publishing digests.

## Single operator, in-cluster

Apply [`deploy/rbac/kubeassist-readonly.yaml`](../deploy/rbac/kubeassist-readonly.yaml), run the backend image with `serviceAccountName: kubeassist-backend`, leave `KUBECONFIG` unset (in-cluster credentials) and `ENABLE_LOCAL_MODE=true`. Do not expose it publicly without authentication in front of it — local mode has no login.
