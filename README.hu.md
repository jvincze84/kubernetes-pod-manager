# Pod Manager

Kis webes felület Kubernetes POD-ok kezelésére (Go + client-go, beágyazott statikus frontend). A felület nyelve angol.

*English version: [README.md](README.md)*

## Funkciók

- Namespace lista élő kereséssel (Enter → első találat megnyitása), namespace-enként Running / hibás POD szám
- Namespace nézet
  - **List all pods** / **Delete all pods** (megerősítő kérdéssel)
  - Deployment / StatefulSet / DaemonSet lista Ready-állapottal
  - Soronként: **Delete pods**, **Rollout restart**, Deploymentnél **Scale to 0** / **Scale to 1**
- **Clean**: a namespace összes Completed (Succeeded) POD-jának törlése
- Workload nézet: a hozzá tartozó POD-ok (label selector alapján), POD törlése, Terminating POD-nál force törlés
- POD státusz (`kubectl get pods`-szal azonos STATUS logika), Ready, restartok, kor, node, IP, konténerállapotok
- Logok: konténerválasztó, tail, follow (élő stream), előző konténer (`--previous`), időbélyeg, szűrés, letöltés
- Auto-frissítés 5 másodpercenként

A Deploymentet/STS-t/DS-t az app nem törli. Módosítás csak kettő van: a Scale to 0/1 (csak Deployment, a scale subresource-on át), és a rollout restart, ami
pontosan azt csinálja, mint a `kubectl rollout restart`: a `kubectl.kubernetes.io/restartedAt` annotációt patch-eli a pod template-re.

## Build

```bash
docker build -t my.registry/pod-manager:1.0.0 .
docker push my.registry/pod-manager:1.0.0
```

Multi-arch: `docker buildx build --platform linux/amd64,linux/arm64 -t my.registry/pod-manager:1.0.0 --push .`

## Telepítés

1. Állítsd be az image-et a `deploy/02-deployment.yaml`-ban, a hostot pedig a `deploy/05-ingress.yaml`-ban.
2. Namespace + basic auth secret:

   ```bash
   kubectl apply -f deploy/00-namespace.yaml
   kubectl -n pod-manager create secret generic pod-manager-auth \
     --from-literal=admin="$(openssl passwd -6 'ErosJelszo')"
   # több felhasználó: további --from-literal=user2="$(openssl passwd -6 '...')"
   ```

3. A többi erőforrás:

   ```bash
   kubectl apply -k deploy/
   ```

### HAProxy ingress controller

A manifest a **haproxytech/kubernetes-ingress** annotációit használja (`haproxy.org/auth-*`); itt a secret
kulcsa a felhasználónév, az értéke pedig crypt hash.

Ha **jcmoraisjr/haproxy-ingress**-t használsz, kapcsold át az Ingressben a kikommentezett
`haproxy-ingress.github.io/*` annotációkra, a secretet pedig htpasswd formátumban, `auth` kulccsal hozd létre:

```bash
htpasswd -nbB admin 'ErosJelszo' > auth
kubectl -n pod-manager create secret generic pod-manager-auth --from-file=auth
```

Élesben érdemes TLS-t bekapcsolni (az Ingress `tls` blokkja), mert a basic auth jelszava egyébként titkosítatlanul utazik.

## Biztonság

- Az alkalmazás **nem** hitelesít, ezt az Ingress végzi. A Service-t ne tedd ki máshogy (NodePort/LoadBalancer)!
- RBAC (ClusterRole): `namespaces` get/list; `pods` get/list/delete; `pods/log` get;
  `deployments/statefulsets/daemonsets` get/list/patch; `deployments/scale` patch. Ha csak bizonyos namespace-ekhez kell hozzáférés,
  a namespace-listához tartsd meg a ClusterRole-t, a többi jogot pedig Role/RoleBinding párokkal add meg namespace-enként.
- CSRF védelem: minden módosító kérés csak `X-Requested-With: pod-manager` fejléccel megy át.
- A módosító műveletek JSON formátumban a stdout-ra logolódnak.

## Helyi futtatás

```bash
go run .        # a ~/.kube/config-ot (vagy a KUBECONFIG-ot) használja, a felület: http://localhost:8080
```

Az ellenőrzéshez nem kell helyben Go, minden Dockerben fut:

```bash
scripts/check.sh         # go vet + gofmt, JS szintaxis, image build, smoke teszt
scripts/check.sh tidy    # go mod tidy
scripts/smoke-test.sh    # csak a smoke teszt (cluster nélkül, elérhetetlen API szerver ellen)
```

## Telepítés telefonra (PWA)

Az app PWA-ként telepíthető (Android Chrome: menü → „Alkalmazás telepítése” / „Hozzáadás a kezdőképernyőhöz”). Feltételek:

- **HTTPS kötelező**: kapcsold be a TLS-t az Ingressben. Sima HTTP-n a böngésző nem engedi telepíteni.
- Először jelentkezz be a böngészőben (basic auth), utána telepíts. A manifestet `crossorigin="use-credentials"`
  attribútummal töltöm be, így a böngésző a hitelesítési adatokat erre a kérésre is elküldi.
- Offline módot nem támogat (service worker nincs), hiszen élő cluster-adatok nélkül úgysem lenne használható.
