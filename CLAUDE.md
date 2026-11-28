# CLAUDE.md – Pod Manager

Read this first when resuming work. User-facing docs: `README.md` (English) and `README.hu.md` (Hungarian).

## Communication

- The user writes in **Hungarian**. Answer in Hungarian.
- Code identifiers are English. Code comments are Hungarian, as is the existing style.
- **UI text is English** (switched from Hungarian on 2026-11-22). Docs: English `README.md` plus Hungarian `README.hu.md`,
  where button names are quoted in English to match the UI.

## What this is

A small Kubernetes web app for managing **pods**:

- Go backend using client-go (`main.go`, a single file)
- Vanilla-JS frontend embedded with `go:embed` (`web/`)
- Plain manifests (`deploy/`, no Helm). Auth is HAProxy Ingress basic auth (`ingressClassName: haproxy`)

## Hard rules (requirements agreed with the user)

1. **Pods only.** Never add workload delete or edit features. The only allowed workload mutations:
   - rollout restart (patching the `kubectl.kubernetes.io/restartedAt` annotation on the pod template)
   - Scale to 0 / Scale to 1: **Deployments only**, via the `scale` subresource, and the server rejects any
     other value
2. **The app does no authentication.** The Ingress does it. Don't add login logic unless asked.
3. **Every new Kubernetes API call** must also be added to the ClusterRole (`deploy/01-rbac.yaml`) and to
   the RBAC list in **both** READMEs.
4. **Keep `README.md` and `README.hu.md` in sync** (features, API table, deploy steps).
5. **Destructive endpoints are validated on the server too**, not just in the UI:
   - delete-all requires `?confirm=<ns>`
   - Clean filters on `status.phase=Succeeded` on the server
   - workloads with an empty selector are refused (they would match every pod)
6. **Security conventions:**
   - Every mutating endpoint uses a non-GET method. The middleware then requires
     `X-Requested-With: pod-manager` (CSRF) and logs the action.
   - The CSP forbids inline scripts. All JS goes in `web/app.js`, with no `onclick=` attributes. Use event
     delegation with `data-*` attributes.
   - Any API data put into HTML goes through `esc()`. Use `enc()` for URL segments.
7. No Helm chart, no frontend build step or npm dependencies, no extra Go dependencies beyond client-go
   unless clearly justified.

## Environment facts

- **Go, make, kubectl and node are NOT installed locally.** Docker is available, and all tooling runs in containers.
- **No Kubernetes cluster is available.** The app has never run against a real cluster here. Say so
  honestly when reporting, and ask the user to test cluster behaviour.
- The Go version is pinned by client-go (v0.37 needs Go ≥ 1.26). The `golang:1.26-alpine` image in the
  `Dockerfile` and in `scripts/check.sh` must match `go.mod`.

## Verify every change

```bash
scripts/check.sh          # go vet + gofmt, node --check app.js, docker build, smoke test → must print ALL OK
scripts/check.sh tidy     # after adding or removing imports
```

`scripts/smoke-test.sh` runs the image against an unreachable API server (fake kubeconfig) and checks HTTP
codes. **When you add an endpoint, add a smoke check** for its validation and CSRF behaviour. Go 1.22+ mux
pattern conflicts panic at startup, and the smoke test catches that.

## Architecture notes

**Backend (`main.go`)**

- `loadConfig()`: uses the in-cluster config, falling back to kubeconfig for local `go run .`.
- Routes are registered in `main()` with Go 1.22 method+path patterns.
- `podStatus()` ports the STATUS column logic of `kubectl get pods`, including init containers, native
  sidecars and Terminating.
- `errorStatus` (regex in Go) decides which pods count as "failing" for the namespace counters. It **must
  stay in sync** with `statusClass()` in `web/app.js`, which uses the same regex for red badges.
- Namespace counters: running / pending / failing. `isPendingStatus()` = anything not Running, Completed,
  Succeeded, Terminating or matching `errorStatus`. Pending older than `stuckAfter` (5 min) is flagged red.
- The namespace list makes a single cluster-wide pod List with `ResourceVersion: "0"`, served from the API
  server's cache, to get running/failing counts.
- `resolveWorkload()` gets workload info plus its pods via the label selector. It is used by the detail,
  delete-pods and similar handlers.
- Log streaming copies with an `http.Flusher`. The server sets no `WriteTimeout` because follow streams are
  long-lived.

**Frontend (`web/app.js`)**

- Hash router with three routes:
  - `#/`: namespaces
  - `#/ns/{ns}[/pods]`: namespace view, workloads or pods tab
  - `#/ns/{ns}/{kind}/{name}`: workload view
- Each `viewX()` renders its skeleton once and returns `{ load }`. The 5 s auto-refresh calls `load()`.
- The namespace view always fetches **both** workloads and pods. An earlier bug left the workload counter
  at 0 on the pods tab.
- Shared helpers:
  - `podTable()` and `bindPodActions()`: pod rows and their actions
  - `workloadAction()`: delete-pods, restart, scale
  - `confirmDialog()`: returns a Promise
- Log modal: streams via `fetch` and a `ReadableStream`. The buffer is capped at about 5 MB, and the filter
  runs on the client.

**PWA:** `web/manifest.json` plus PNG icons in `web/icons/` (generated with a small Python script, a
helm-wheel shape). The manifest link has `crossorigin="use-credentials"`, otherwise basic auth makes the
manifest fetch fail with 401. There is no service worker on purpose. Installing requires HTTPS.

## Placeholders the user must replace

- `go.mod` module path `github.com/example/pod-manager`: change it once the real repo URL is known
- `registry.example.com/pod-manager:latest` in `deploy/02-deployment.yaml`
- `pod-manager.example.com` in `deploy/05-ingress.yaml` (the TLS block is commented out)

## Open / unverified

- Never tested on a real cluster. That covers pod-status edge cases, log follow through HAProxy, and the
  namespace counts.
- The `haproxy.org/timeout-server` annotation on the Ingress is unverified. Some controllers only honour
  it in the ConfigMap. If log follow drops after about a minute, that is the cause.
- PWA installation has not been tested on a real Android device.
- The cluster-wide pod list on every namespace-page refresh could be heavy on very large clusters.

## History

- 2026-11-12: initial version. Namespace list and search, workloads, pod delete/logs, rollout restart,
  manifests, Dockerfile.
- 2026-11-12: fixed the workload counter on the pods tab. Added the Clean (Completed pods) button.
- 2026-11-12: running/failing counts per namespace. Delete-all asks for a simple confirmation instead of a
  typed name. Added Deployment Scale to 0/1.
- 2026-11-12: PWA manifest and icons, mobile layout tweaks.
- 2026-11-20: git prep (`.gitignore`), `scripts/check.sh` and `scripts/smoke-test.sh`, English
  `README.md` (the Hungarian one moved to `README.hu.md`), this file.
- 2026-11-22: UI translated to English (`web/app.js`, `web/index.html`, `web/manifest.json`).
- 2026-11-28: namespace counters now running / pending / failing, with stuck-pending (>5 min) highlight.
