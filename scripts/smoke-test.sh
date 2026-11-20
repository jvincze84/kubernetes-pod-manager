#!/usr/bin/env bash
# Smoke test without a real cluster: runs the image against an unreachable API
# server and checks static assets, health, CSRF protection and input validation.
# Usage: scripts/smoke-test.sh [image]   (default: pod-manager:dev)
set -euo pipefail

IMAGE="${1:-pod-manager:dev}"
PORT="${PORT:-18080}"
NAME="pod-manager-smoke-$$"
TMP="$(mktemp -d)"
trap 'docker rm -f "$NAME" >/dev/null 2>&1 || true; rm -rf "$TMP"' EXIT

cat > "$TMP/kubeconfig" <<'KC'
apiVersion: v1
kind: Config
clusters: [{name: x, cluster: {server: "https://127.0.0.1:1"}}]
users: [{name: x, user: {token: t}}]
contexts: [{name: x, context: {cluster: x, user: x}}]
current-context: x
KC
chmod 644 "$TMP/kubeconfig"

docker run -d --name "$NAME" -p "$PORT:8080" -v "$TMP/kubeconfig:/kc:ro" -e KUBECONFIG=/kc "$IMAGE" >/dev/null
for _ in $(seq 1 30); do curl -sf "localhost:$PORT/healthz" >/dev/null && break; sleep 0.2; done

BASE="http://localhost:$PORT"
H='X-Requested-With: pod-manager'
fail=0
check() { # name expected_code curl-args...
  local name="$1" want="$2"; shift 2
  local got; got="$(curl -s -o /dev/null -w '%{http_code}' "$@")"
  if [[ "$got" == "$want" ]]; then echo "ok   $name ($got)"; else echo "FAIL $name: want $want, got $got"; fail=1; fi
}

check "healthz"                    200 "$BASE/healthz"
check "index.html"                 200 "$BASE/"
check "app.js"                     200 "$BASE/app.js"
check "manifest.json"              200 "$BASE/manifest.json"
check "icon-512"                   200 "$BASE/icons/icon-512.png"
check "API reaches k8s client"     500 "$BASE/api/namespaces"
check "CSRF: POST without header"  403 -X POST "$BASE/api/namespaces/x/clean-completed"
check "delete-all needs confirm"   400 -X DELETE -H "$H" "$BASE/api/namespaces/x/pods"
check "bad workload kind"          400 -X POST -H "$H" "$BASE/api/namespaces/x/workloads/foo/y/restart"
check "scale only 0/1"             400 -X POST -H "$H" "$BASE/api/namespaces/x/workloads/deployment/y/scale?replicas=5"
check "scale only deployments"     405 -X POST -H "$H" "$BASE/api/namespaces/x/workloads/statefulset/y/scale?replicas=0"

if docker logs "$NAME" 2>&1 | grep -qi panic; then echo "FAIL panic in logs"; fail=1; fi
exit "$fail"
