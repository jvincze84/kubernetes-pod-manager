# Pod Manager

A small, self-contained web UI for managing Kubernetes **pods**: Go + client-go backend, with a static
frontend embedded into a single binary. The image is about 44 MB, distroless, and runs as non-root.

*Magyar változat: [README.hu.md](README.hu.md)*

## Features

- **Namespace list** with live search (Enter opens the first match), and per-namespace pod counts
  (`running / pending / failing`; the pending number turns red with a `!` when a pod has been pending for over 5 minutes), a legend below the search box, and a filter for namespaces with unhealthy pods (a pod that is not Running, or Running with not all containers ready; Completed pods are ignored)
- **Namespace view**
  - **List all pods** / **Delete all pods** (asks for confirmation first)
  - **Clean**: deletes every Completed (`Succeeded`) pod in the namespace
  - Deployments, StatefulSets and DaemonSets with their ready state
  - Per workload: **Delete pods** and **Rollout restart**. Deployments also get **Scale to 0** and **Scale to 1**
- **Workload view**: the workload's pods (matched by label selector). Single pods can be deleted, and
  Terminating pods can be force-deleted
- **Pod status** uses the same STATUS logic as `kubectl get pods`. Each pod also shows ready count,
  restarts, age, node, IP and per-container state
- **Logs**: container picker, tail size, follow (live stream), previous container (`--previous`),
  timestamps, client-side filter and download
- Auto-refresh every 5 seconds
- Installable as a **PWA** (see below)

### Scope

The app manages pods only. It never deletes or edits workloads, with exactly two exceptions:

- **Rollout restart** does the same thing as `kubectl rollout restart`: it patches the
  `kubectl.kubernetes.io/restartedAt` annotation onto the pod template.
- **Scale to 0 / Scale to 1** works for Deployments only, through the `scale` subresource. The server
  rejects any other replica count.

## Build

```bash
docker build -t my.registry/pod-manager:1.0.0 .
docker push my.registry/pod-manager:1.0.0
```

Multi-arch build:
`docker buildx build --platform linux/amd64,linux/arm64 -t my.registry/pod-manager:1.0.0 --push .`

## Deploy

Plain manifests plus a Kustomization live in `deploy/`. There is no Helm chart.

1. Set the image in `deploy/02-deployment.yaml` and the host in `deploy/05-ingress.yaml`.
2. Create the namespace and the basic-auth secret:

   ```bash
   kubectl apply -f deploy/00-namespace.yaml
   kubectl -n pod-manager create secret generic pod-manager-auth \
     --from-literal=admin="$(openssl passwd -6 'StrongPassword')"
   # more users: add --from-literal=user2="$(openssl passwd -6 '...')"
   ```

3. Apply everything else:

   ```bash
   kubectl apply -k deploy/
   ```

The secret is intentionally left out of the Kustomization, so that no default password is ever deployed.
`deploy/04-auth-secret.example.yaml` documents its format.

### HAProxy ingress controller

The Ingress uses `ingressClassName: haproxy` and the **haproxytech/kubernetes-ingress** annotations
(`haproxy.org/auth-*`). With that controller, each key in the secret is a username and each value is a
crypt(3) hash.

If you run **jcmoraisjr/haproxy-ingress**, switch to the commented-out `haproxy-ingress.github.io/*`
annotations in the Ingress. That controller needs the secret in htpasswd format, under an `auth` key:

```bash
htpasswd -nbB admin 'StrongPassword' > auth
kubectl -n pod-manager create secret generic pod-manager-auth --from-file=auth
```

**Enable TLS** with the Ingress `tls` block. Without it, the basic-auth credentials travel in clear text,
and the PWA cannot be installed.

## Security

- **The app has no authentication of its own.** The Ingress handles it. Do not expose the Service any other
  way, such as NodePort or LoadBalancer.
- The ClusterRole grants:
  - `namespaces`: get, list
  - `pods`: get, list, delete
  - `pods/log`: get
  - `deployments`, `statefulsets`, `daemonsets`: get, list, patch
  - `deployments/scale`: patch

  To restrict the app to certain namespaces, keep a ClusterRole for listing namespaces and grant
  everything else per namespace with Role/RoleBinding pairs.
- **CSRF protection:** every mutating request must carry the `X-Requested-With: pod-manager` header.
  Browsers send basic-auth credentials automatically, and a cross-site page cannot set a custom header.
- The UI uses a strict Content-Security-Policy with no inline scripts.
- Mutating actions are logged to stdout as JSON.

## Install on a phone (PWA)

On Android Chrome, open the menu and choose "Install app" or "Add to Home screen". Requirements:

- **HTTPS is mandatory.** Browsers refuse to install over plain HTTP.
- Log in first (basic auth), then install. The manifest is loaded with `crossorigin="use-credentials"`, so
  the credentials are sent for that request too.
- There is no offline mode and no service worker. The app is useless without live cluster data anyway.

## Local development

```bash
go run .                 # uses ~/.kube/config (or $KUBECONFIG), UI at http://localhost:8080
```

You do not need Go installed for verification, because everything runs in Docker:

```bash
scripts/check.sh         # go vet + gofmt, JS syntax check, image build, smoke test
scripts/check.sh tidy    # go mod tidy
scripts/smoke-test.sh    # smoke test only. It needs no cluster: the image runs against an unreachable API server
```

## Project layout

```
main.go            backend: HTTP API, Kubernetes calls, pod status logic
web/               frontend embedded via go:embed (index.html, app.js, style.css, PWA manifest, icons)
deploy/            Kubernetes manifests + kustomization.yaml
scripts/           check.sh, smoke-test.sh
Dockerfile         multi-stage build -> distroless static image
```

## HTTP API

Every mutating request requires the `X-Requested-With: pod-manager` header.

| Method | Path | Description |
|---|---|---|
| GET | `/healthz` | Liveness/readiness probe |
| GET | `/api/namespaces` | Namespaces with running/pending/failing pod counts |
| GET | `/api/namespaces/{ns}/pods` | Pods in a namespace |
| DELETE | `/api/namespaces/{ns}/pods?confirm={ns}` | Delete all pods in a namespace |
| POST | `/api/namespaces/{ns}/clean-completed` | Delete Completed (`Succeeded`) pods |
| DELETE | `/api/namespaces/{ns}/pods/{pod}[?force=true]` | Delete one pod (`force` = grace period 0) |
| GET | `/api/namespaces/{ns}/pods/{pod}/logs` | Logs. Query: `container`, `tail`, `follow`, `previous`, `timestamps`, `download` |
| GET | `/api/namespaces/{ns}/workloads` | Deployments, StatefulSets, DaemonSets |
| GET | `/api/namespaces/{ns}/workloads/{kind}/{name}` | Workload details and its pods |
| DELETE | `/api/namespaces/{ns}/workloads/{kind}/{name}/pods` | Delete the workload's pods |
| POST | `/api/namespaces/{ns}/workloads/{kind}/{name}/restart` | Rollout restart |
| POST | `/api/namespaces/{ns}/workloads/deployment/{name}/scale?replicas=0\|1` | Scale a Deployment to 0 or 1 |

`{kind}` is `deployment`, `statefulset` or `daemonset`.
