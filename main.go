// Pod Manager – egyszerű webes felület Kubernetes POD-ok kezeléséhez.
//
// Képességek:
//   - namespace-ek listázása
//   - Deployment / StatefulSet / DaemonSet listázása, rollout restart
//   - POD-ok listázása, státusza, törlése, logjainak megtekintése
//
// Hitelesítés: az Ingress (HAProxy basic auth) végzi, az alkalmazás nem.
package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"sort"
	"strconv"
	"syscall"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

//go:embed web
var webFS embed.FS

// csrfHeader: minden módosító kérésnél kötelező. Mivel a basic auth-ot a böngésző
// automatikusan küldi, ez védi az alkalmazást a cross-site kérések ellen
// (egyedi fejlécet idegen oldal nem tud CORS preflight nélkül küldeni).
const (
	csrfHeader = "X-Requested-With"
	csrfValue  = "pod-manager"
)

type server struct {
	cs kubernetes.Interface
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	cfg, err := loadConfig()
	if err != nil {
		slog.Error("kubernetes config", "err", err)
		os.Exit(1)
	}
	cfg.QPS, cfg.Burst = 50, 100
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		slog.Error("kubernetes client", "err", err)
		os.Exit(1)
	}
	s := &server{cs: cs}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "ok") })
	mux.HandleFunc("GET /api/namespaces", s.listNamespaces)
	mux.HandleFunc("GET /api/namespaces/{ns}/pods", s.listPods)
	mux.HandleFunc("DELETE /api/namespaces/{ns}/pods", s.deleteAllPods)
	mux.HandleFunc("POST /api/namespaces/{ns}/clean-completed", s.cleanCompletedPods)
	mux.HandleFunc("DELETE /api/namespaces/{ns}/pods/{pod}", s.deletePod)
	mux.HandleFunc("GET /api/namespaces/{ns}/pods/{pod}/logs", s.podLogs)
	mux.HandleFunc("GET /api/namespaces/{ns}/workloads", s.listWorkloads)
	mux.HandleFunc("GET /api/namespaces/{ns}/workloads/{kind}/{name}", s.getWorkload)
	mux.HandleFunc("DELETE /api/namespaces/{ns}/workloads/{kind}/{name}/pods", s.deleteWorkloadPods)
	mux.HandleFunc("POST /api/namespaces/{ns}/workloads/{kind}/{name}/restart", s.restartWorkload)
	mux.HandleFunc("POST /api/namespaces/{ns}/workloads/deployment/{name}/scale", s.scaleDeployment)

	sub, _ := fs.Sub(webFS, "web")
	mux.Handle("GET /", http.FileServerFS(sub))

	addr := envOr("LISTEN_ADDR", ":8080")
	srv := &http.Server{
		Addr:              addr,
		Handler:           withMiddleware(mux),
		ReadHeaderTimeout: 10 * time.Second,
		// WriteTimeout szándékosan nincs: a log follow hosszan futó stream.
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		slog.Info("listening", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("listen", "err", err)
			os.Exit(1)
		}
	}()
	<-ctx.Done()
	shCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shCtx)
}

func loadConfig() (*rest.Config, error) {
	if cfg, err := rest.InClusterConfig(); err == nil {
		return cfg, nil
	}
	// Helyi fejlesztéshez: ~/.kube/config vagy KUBECONFIG
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{}).ClientConfig()
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func withMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:")
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if r.Header.Get(csrfHeader) != csrfValue {
				writeErr(w, http.StatusForbidden, "missing or invalid "+csrfHeader+" header")
				return
			}
			slog.Info("action", "method", r.Method, "path", r.URL.Path,
				"remote", r.RemoteAddr, "forwarded_for", r.Header.Get("X-Forwarded-For"))
		}
		next.ServeHTTP(w, r)
	})
}

// ---------- válasz segédek ----------

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func writeK8sErr(w http.ResponseWriter, err error) {
	code := http.StatusInternalServerError
	var st apierrors.APIStatus
	if errors.As(err, &st) && st.Status().Code != 0 {
		code = int(st.Status().Code)
	}
	writeErr(w, code, err.Error())
}

// ---------- namespace-ek ----------

type nsInfo struct {
	Name    string    `json:"name"`
	Status  string    `json:"status"`
	Created time.Time `json:"created"`
	Running int       `json:"running"`
	Pending int       `json:"pending"` // még nem indult el (Pending, ContainerCreating, Init:N/M ...)
	Stuck   int       `json:"stuck"`   // a Pending podok közül a stuckAfter-nél régebbiek
	Errors  int       `json:"errors"`
}

// stuckAfter: ennyi idő után számít egy még el nem indult pod "beragadtnak".
const stuckAfter = 5 * time.Minute

// isPendingStatus: a podStatus() eredménye alapján a pod még nem indult el, de nem is hibás.
// A Completed/Succeeded és a Terminating szándékosan nem számít bele.
func isPendingStatus(st string) bool {
	switch st {
	case "Running", "Completed", "Succeeded", "Terminating":
		return false
	}
	return !errorStatus.MatchString(st)
}

// errorStatus: a podStatus() által adott státuszok közül ezek számítanak hibásnak
// (ugyanaz a szabály, mint a frontend piros jelölésénél).
var errorStatus = regexp.MustCompile(`(?i)Err|BackOff|Fail|OOM|Evicted|Unknown|ExitCode|Signal|NotReady|Invalid|Lost`)

func (s *server) listNamespaces(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	list, err := s.cs.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		writeK8sErr(w, err)
		return
	}
	// Egyetlen, cluster szintű POD lista. A ResourceVersion "0" miatt az API szerver
	// a saját cache-éből szolgálja ki, nem terheli az etcd-t.
	pods, err := s.cs.CoreV1().Pods("").List(ctx, metav1.ListOptions{ResourceVersion: "0"})
	if err != nil {
		writeK8sErr(w, err)
		return
	}
	type counts struct{ running, pending, stuck, errors int }
	now := time.Now()
	byNS := map[string]*counts{}
	for i := range pods.Items {
		p := &pods.Items[i]
		c := byNS[p.Namespace]
		if c == nil {
			c = &counts{}
			byNS[p.Namespace] = c
		}
		switch st := podStatus(p); {
		case st == "Running":
			c.running++
		case errorStatus.MatchString(st):
			c.errors++
		case isPendingStatus(st):
			c.pending++
			if now.Sub(p.CreationTimestamp.Time) > stuckAfter {
				c.stuck++
			}
		}
	}
	out := make([]nsInfo, 0, len(list.Items))
	for _, n := range list.Items {
		ni := nsInfo{Name: n.Name, Status: string(n.Status.Phase), Created: n.CreationTimestamp.Time}
		if c := byNS[n.Name]; c != nil {
			ni.Running, ni.Pending, ni.Stuck, ni.Errors = c.running, c.pending, c.stuck, c.errors
		}
		out = append(out, ni)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	writeJSON(w, http.StatusOK, out)
}

// ---------- POD-ok ----------

type containerInfo struct {
	Name     string `json:"name"`
	Image    string `json:"image"`
	Init     bool   `json:"init,omitempty"`
	Ready    bool   `json:"ready"`
	State    string `json:"state"`
	Restarts int32  `json:"restarts"`
}

type podInfo struct {
	Name       string          `json:"name"`
	Status     string          `json:"status"`
	Phase      string          `json:"phase"`
	Ready      string          `json:"ready"`
	Restarts   int32           `json:"restarts"`
	Node       string          `json:"node"`
	IP         string          `json:"ip"`
	Created    time.Time       `json:"created"`
	Owner      string          `json:"owner"`
	Containers []containerInfo `json:"containers"`
}

func toPodInfo(p *corev1.Pod) podInfo {
	pi := podInfo{
		Name:    p.Name,
		Status:  podStatus(p),
		Phase:   string(p.Status.Phase),
		Node:    p.Spec.NodeName,
		IP:      p.Status.PodIP,
		Created: p.CreationTimestamp.Time,
	}
	if ref := metav1.GetControllerOf(p); ref != nil {
		pi.Owner = ref.Kind + "/" + ref.Name
	}

	statuses := map[string]corev1.ContainerStatus{}
	for _, cs := range p.Status.InitContainerStatuses {
		statuses["init:"+cs.Name] = cs
	}
	for _, cs := range p.Status.ContainerStatuses {
		statuses[cs.Name] = cs
	}
	add := func(c corev1.Container, init bool) {
		key := c.Name
		if init {
			key = "init:" + c.Name
		}
		ci := containerInfo{Name: c.Name, Image: c.Image, Init: init, State: "Waiting"}
		if cs, ok := statuses[key]; ok {
			ci.Ready = cs.Ready
			ci.Restarts = cs.RestartCount
			ci.State = containerState(cs.State)
		}
		pi.Containers = append(pi.Containers, ci)
	}
	for _, c := range p.Spec.InitContainers {
		add(c, true)
	}
	ready := 0
	for _, c := range p.Spec.Containers {
		add(c, false)
	}
	for _, cs := range p.Status.ContainerStatuses {
		if cs.Ready {
			ready++
		}
		pi.Restarts += cs.RestartCount
	}
	for _, cs := range p.Status.InitContainerStatuses {
		pi.Restarts += cs.RestartCount
	}
	pi.Ready = fmt.Sprintf("%d/%d", ready, len(p.Spec.Containers))
	return pi
}

func containerState(st corev1.ContainerState) string {
	switch {
	case st.Running != nil:
		return "Running"
	case st.Waiting != nil:
		if st.Waiting.Reason != "" {
			return st.Waiting.Reason
		}
		return "Waiting"
	case st.Terminated != nil:
		if st.Terminated.Reason != "" {
			return st.Terminated.Reason
		}
		return fmt.Sprintf("Terminated (exit %d)", st.Terminated.ExitCode)
	}
	return "Unknown"
}

// podStatus a `kubectl get pods` STATUS oszlopának logikáját követi.
func podStatus(p *corev1.Pod) string {
	reason := string(p.Status.Phase)
	if p.Status.Reason != "" {
		reason = p.Status.Reason
	}

	sidecar := map[string]bool{}
	for _, c := range p.Spec.InitContainers {
		if c.RestartPolicy != nil && *c.RestartPolicy == corev1.ContainerRestartPolicyAlways {
			sidecar[c.Name] = true
		}
	}

	initializing := false
	for i, c := range p.Status.InitContainerStatuses {
		switch {
		case c.State.Terminated != nil && c.State.Terminated.ExitCode == 0:
			continue
		case sidecar[c.Name] && c.Started != nil && *c.Started:
			continue
		case c.State.Terminated != nil:
			switch {
			case c.State.Terminated.Reason != "":
				reason = "Init:" + c.State.Terminated.Reason
			case c.State.Terminated.Signal != 0:
				reason = fmt.Sprintf("Init:Signal:%d", c.State.Terminated.Signal)
			default:
				reason = fmt.Sprintf("Init:ExitCode:%d", c.State.Terminated.ExitCode)
			}
		case c.State.Waiting != nil && c.State.Waiting.Reason != "" && c.State.Waiting.Reason != "PodInitializing":
			reason = "Init:" + c.State.Waiting.Reason
		default:
			reason = fmt.Sprintf("Init:%d/%d", i, len(p.Spec.InitContainers))
		}
		initializing = true
		break
	}

	if !initializing {
		hasRunning := false
		for i := len(p.Status.ContainerStatuses) - 1; i >= 0; i-- {
			c := p.Status.ContainerStatuses[i]
			switch {
			case c.State.Waiting != nil && c.State.Waiting.Reason != "":
				reason = c.State.Waiting.Reason
			case c.State.Terminated != nil && c.State.Terminated.Reason != "":
				reason = c.State.Terminated.Reason
			case c.State.Terminated != nil && c.State.Terminated.Signal != 0:
				reason = fmt.Sprintf("Signal:%d", c.State.Terminated.Signal)
			case c.State.Terminated != nil:
				reason = fmt.Sprintf("ExitCode:%d", c.State.Terminated.ExitCode)
			case c.Ready && c.State.Running != nil:
				hasRunning = true
			}
		}
		if reason == "Completed" && hasRunning {
			reason = "NotReady"
			for _, cond := range p.Status.Conditions {
				if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
					reason = "Running"
				}
			}
		}
	}

	if p.DeletionTimestamp != nil {
		if p.Status.Reason == "NodeLost" {
			reason = "Unknown"
		} else {
			reason = "Terminating"
		}
	}
	return reason
}

func podInfos(pods []corev1.Pod) []podInfo {
	out := make([]podInfo, 0, len(pods))
	for i := range pods {
		out = append(out, toPodInfo(&pods[i]))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (s *server) listPods(w http.ResponseWriter, r *http.Request) {
	list, err := s.cs.CoreV1().Pods(r.PathValue("ns")).List(r.Context(), metav1.ListOptions{})
	if err != nil {
		writeK8sErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, podInfos(list.Items))
}

type deleteResult struct {
	Deleted []string          `json:"deleted"`
	Failed  map[string]string `json:"failed,omitempty"`
}

func (s *server) deletePods(ctx context.Context, ns string, pods []corev1.Pod) deleteResult {
	res := deleteResult{Deleted: []string{}, Failed: map[string]string{}}
	for _, p := range pods {
		err := s.cs.CoreV1().Pods(ns).Delete(ctx, p.Name, metav1.DeleteOptions{})
		if err != nil && !apierrors.IsNotFound(err) {
			res.Failed[p.Name] = err.Error()
			continue
		}
		res.Deleted = append(res.Deleted, p.Name)
	}
	slog.Info("pods deleted", "namespace", ns, "count", len(res.Deleted), "failed", len(res.Failed))
	return res
}

func (s *server) deleteAllPods(w http.ResponseWriter, r *http.Request) {
	ns := r.PathValue("ns")
	// Biztonsági megerősítés: a kliensnek el kell küldenie a namespace nevét.
	if r.URL.Query().Get("confirm") != ns {
		writeErr(w, http.StatusBadRequest, "confirm query parameter must equal the namespace name")
		return
	}
	list, err := s.cs.CoreV1().Pods(ns).List(r.Context(), metav1.ListOptions{})
	if err != nil {
		writeK8sErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.deletePods(r.Context(), ns, list.Items))
}

// cleanCompletedPods törli a namespace összes Completed (phase=Succeeded) POD-ját.
// A szűrés szerver oldalon történik, így a kliens nem tud véletlenül futó POD-ot törölni vele.
func (s *server) cleanCompletedPods(w http.ResponseWriter, r *http.Request) {
	ns := r.PathValue("ns")
	list, err := s.cs.CoreV1().Pods(ns).List(r.Context(), metav1.ListOptions{
		FieldSelector: "status.phase=" + string(corev1.PodSucceeded),
	})
	if err != nil {
		writeK8sErr(w, err)
		return
	}
	pods := make([]corev1.Pod, 0, len(list.Items))
	for _, p := range list.Items {
		if p.Status.Phase == corev1.PodSucceeded && p.DeletionTimestamp == nil {
			pods = append(pods, p)
		}
	}
	writeJSON(w, http.StatusOK, s.deletePods(r.Context(), ns, pods))
}

func (s *server) deletePod(w http.ResponseWriter, r *http.Request) {
	ns, name := r.PathValue("ns"), r.PathValue("pod")
	opts := metav1.DeleteOptions{}
	if r.URL.Query().Get("force") == "true" {
		var zero int64
		opts.GracePeriodSeconds = &zero
	}
	if err := s.cs.CoreV1().Pods(ns).Delete(r.Context(), name, opts); err != nil {
		writeK8sErr(w, err)
		return
	}
	slog.Info("pod deleted", "namespace", ns, "pod", name, "force", opts.GracePeriodSeconds != nil)
	writeJSON(w, http.StatusOK, deleteResult{Deleted: []string{name}})
}

func (s *server) podLogs(w http.ResponseWriter, r *http.Request) {
	ns, name := r.PathValue("ns"), r.PathValue("pod")
	q := r.URL.Query()

	opts := &corev1.PodLogOptions{
		Container:  q.Get("container"),
		Previous:   q.Get("previous") == "true",
		Timestamps: q.Get("timestamps") == "true",
		Follow:     q.Get("follow") == "true",
	}
	tail := int64(500)
	if v := q.Get("tail"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			tail = n
		}
	}
	if tail > 0 {
		tail = min(tail, 50000)
		opts.TailLines = &tail
	}

	stream, err := s.cs.CoreV1().Pods(ns).GetLogs(name, opts).Stream(r.Context())
	if err != nil {
		writeK8sErr(w, err)
		return
	}
	defer stream.Close()

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	if q.Get("download") == "true" {
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.log"`, name))
	}
	w.WriteHeader(http.StatusOK)

	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 32*1024)
	for {
		n, rerr := stream.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if rerr != nil {
			return
		}
	}
}

// ---------- workloadok ----------

type workloadInfo struct {
	Kind     string    `json:"kind"`
	Name     string    `json:"name"`
	Desired  int32     `json:"desired"`
	Ready    int32     `json:"ready"`
	Updated  int32     `json:"updated"`
	Selector string    `json:"selector"`
	Images   []string  `json:"images"`
	Created  time.Time `json:"created"`
}

func images(spec corev1.PodSpec) []string {
	out := []string{}
	for _, c := range spec.Containers {
		out = append(out, c.Image)
	}
	return out
}

func (s *server) listWorkloads(w http.ResponseWriter, r *http.Request) {
	ns, ctx := r.PathValue("ns"), r.Context()
	apps := s.cs.AppsV1()

	deps, err := apps.Deployments(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		writeK8sErr(w, err)
		return
	}
	stss, err := apps.StatefulSets(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		writeK8sErr(w, err)
		return
	}
	dss, err := apps.DaemonSets(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		writeK8sErr(w, err)
		return
	}

	out := []workloadInfo{}
	for _, d := range deps.Items {
		out = append(out, workloadInfo{Kind: "deployment", Name: d.Name, Desired: ptrOr(d.Spec.Replicas, 1),
			Ready: d.Status.ReadyReplicas, Updated: d.Status.UpdatedReplicas,
			Selector: metav1.FormatLabelSelector(d.Spec.Selector), Images: images(d.Spec.Template.Spec), Created: d.CreationTimestamp.Time})
	}
	for _, st := range stss.Items {
		out = append(out, workloadInfo{Kind: "statefulset", Name: st.Name, Desired: ptrOr(st.Spec.Replicas, 1),
			Ready: st.Status.ReadyReplicas, Updated: st.Status.UpdatedReplicas,
			Selector: metav1.FormatLabelSelector(st.Spec.Selector), Images: images(st.Spec.Template.Spec), Created: st.CreationTimestamp.Time})
	}
	for _, d := range dss.Items {
		out = append(out, workloadInfo{Kind: "daemonset", Name: d.Name, Desired: d.Status.DesiredNumberScheduled,
			Ready: d.Status.NumberReady, Updated: d.Status.UpdatedNumberScheduled,
			Selector: metav1.FormatLabelSelector(d.Spec.Selector), Images: images(d.Spec.Template.Spec), Created: d.CreationTimestamp.Time})
	}
	writeJSON(w, http.StatusOK, out)
}

func ptrOr(p *int32, def int32) int32 {
	if p == nil {
		return def
	}
	return *p
}

// resolveWorkload visszaadja a workload adatait és a hozzá tartozó POD-okat.
func (s *server) resolveWorkload(ctx context.Context, ns, kind, name string) (*workloadInfo, []corev1.Pod, error) {
	apps := s.cs.AppsV1()
	var (
		wi  workloadInfo
		sel *metav1.LabelSelector
	)
	switch kind {
	case "deployment":
		d, err := apps.Deployments(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, nil, err
		}
		sel = d.Spec.Selector
		wi = workloadInfo{Kind: kind, Name: name, Desired: ptrOr(d.Spec.Replicas, 1), Ready: d.Status.ReadyReplicas,
			Updated: d.Status.UpdatedReplicas, Images: images(d.Spec.Template.Spec), Created: d.CreationTimestamp.Time}
	case "statefulset":
		st, err := apps.StatefulSets(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, nil, err
		}
		sel = st.Spec.Selector
		wi = workloadInfo{Kind: kind, Name: name, Desired: ptrOr(st.Spec.Replicas, 1), Ready: st.Status.ReadyReplicas,
			Updated: st.Status.UpdatedReplicas, Images: images(st.Spec.Template.Spec), Created: st.CreationTimestamp.Time}
	case "daemonset":
		d, err := apps.DaemonSets(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, nil, err
		}
		sel = d.Spec.Selector
		wi = workloadInfo{Kind: kind, Name: name, Desired: d.Status.DesiredNumberScheduled, Ready: d.Status.NumberReady,
			Updated: d.Status.UpdatedNumberScheduled, Images: images(d.Spec.Template.Spec), Created: d.CreationTimestamp.Time}
	default:
		return nil, nil, errBadKind
	}

	selector, err := metav1.LabelSelectorAsSelector(sel)
	if err != nil {
		return nil, nil, err
	}
	if selector.Empty() {
		// Üres selector minden POD-ot kiválasztana – ezt nem engedjük.
		return nil, nil, errors.New("workload has an empty selector")
	}
	wi.Selector = selector.String()
	list, err := s.cs.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: wi.Selector})
	if err != nil {
		return nil, nil, err
	}
	return &wi, list.Items, nil
}

var errBadKind = errors.New("kind must be deployment, statefulset or daemonset")

func (s *server) workloadErr(w http.ResponseWriter, err error) {
	if errors.Is(err, errBadKind) {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeK8sErr(w, err)
}

func (s *server) getWorkload(w http.ResponseWriter, r *http.Request) {
	wi, pods, err := s.resolveWorkload(r.Context(), r.PathValue("ns"), r.PathValue("kind"), r.PathValue("name"))
	if err != nil {
		s.workloadErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"workload": wi, "pods": podInfos(pods)})
}

func (s *server) deleteWorkloadPods(w http.ResponseWriter, r *http.Request) {
	ns := r.PathValue("ns")
	_, pods, err := s.resolveWorkload(r.Context(), ns, r.PathValue("kind"), r.PathValue("name"))
	if err != nil {
		s.workloadErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.deletePods(r.Context(), ns, pods))
}

// restartWorkload ugyanazt csinálja, mint a `kubectl rollout restart`:
// annotációt tesz a pod template-re, így a controller sorban újraindítja a POD-okat.
func (s *server) restartWorkload(w http.ResponseWriter, r *http.Request) {
	ns, kind, name := r.PathValue("ns"), r.PathValue("kind"), r.PathValue("name")
	patch := fmt.Appendf(nil, `{"spec":{"template":{"metadata":{"annotations":{"kubectl.kubernetes.io/restartedAt":%q}}}}}`,
		time.Now().Format(time.RFC3339))
	apps := s.cs.AppsV1()
	ctx := r.Context()
	var err error
	switch kind {
	case "deployment":
		_, err = apps.Deployments(ns).Patch(ctx, name, types.StrategicMergePatchType, patch, metav1.PatchOptions{})
	case "statefulset":
		_, err = apps.StatefulSets(ns).Patch(ctx, name, types.StrategicMergePatchType, patch, metav1.PatchOptions{})
	case "daemonset":
		_, err = apps.DaemonSets(ns).Patch(ctx, name, types.StrategicMergePatchType, patch, metav1.PatchOptions{})
	default:
		err = errBadKind
	}
	if err != nil {
		s.workloadErr(w, err)
		return
	}
	slog.Info("rollout restart", "namespace", ns, "kind", kind, "name", name)
	writeJSON(w, http.StatusOK, map[string]string{"status": "restarted"})
}

// scaleDeployment: csak Deploymenthez, és csak 0-ra vagy 1-re (a "scale" subresource-on keresztül).
func (s *server) scaleDeployment(w http.ResponseWriter, r *http.Request) {
	ns, name := r.PathValue("ns"), r.PathValue("name")
	replicas := r.URL.Query().Get("replicas")
	if replicas != "0" && replicas != "1" {
		writeErr(w, http.StatusBadRequest, "replicas must be 0 or 1")
		return
	}
	patch := []byte(`{"spec":{"replicas":` + replicas + `}}`)
	_, err := s.cs.AppsV1().Deployments(ns).Patch(r.Context(), name, types.MergePatchType, patch, metav1.PatchOptions{}, "scale")
	if err != nil {
		writeK8sErr(w, err)
		return
	}
	slog.Info("deployment scaled", "namespace", ns, "name", name, "replicas", replicas)
	writeJSON(w, http.StatusOK, map[string]string{"status": "scaled", "replicas": replicas})
}
