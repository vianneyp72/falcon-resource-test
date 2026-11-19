// Package discover finds Falcon workloads in the cluster. It lists
// DaemonSets and Deployments (paged, nothing cached) and classifies them by
// image name or app.kubernetes.io/name label.
package discover

import (
	"context"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	appsv1client "k8s.io/client-go/kubernetes/typed/apps/v1"

	"github.com/vianneyp72/falcon-cloud-security-labs/tools/falcon-footprint/internal/model"
)

type Workload struct {
	Component model.Component `json:"component"`
	Namespace string          `json:"namespace"`
	Kind      string          `json:"kind"` // DaemonSet | Deployment
	Name      string          `json:"name"`
}

type Result struct {
	Workloads  []Workload `json:"workloads"`
	Namespaces []string   `json:"namespaces"`
}

var byName = map[string]model.Component{
	"falcon-sensor":        model.DaemonSetSensor,
	"falcon-kac":           model.KAC,
	"falcon-imageanalyzer": model.IAR,
	"falcon-container":     model.Injector,
	"falcon-operator":      model.Operator,
}

// ImageName returns the last path segment of an image ref, without tag or digest.
func ImageName(ref string) string {
	if i := strings.Index(ref, "@"); i >= 0 {
		ref = ref[:i]
	}
	if i := strings.LastIndex(ref, "/"); i >= 0 {
		ref = ref[i+1:]
	}
	if i := strings.Index(ref, ":"); i >= 0 {
		ref = ref[:i]
	}
	return ref
}

// Classify decides which Falcon component a workload is. kind is the owning
// workload kind ("DaemonSet" or "Deployment"). The sensor only counts as a
// DaemonSet; falcon-container only counts as the injector Deployment.
func Classify(kind string, images []string, labels map[string]string) (model.Component, bool) {
	cand := []string{labels["app.kubernetes.io/name"]}
	for _, im := range images {
		cand = append(cand, ImageName(im))
	}
	for _, n := range cand {
		c, ok := byName[n]
		if !ok {
			continue
		}
		if c == model.DaemonSetSensor && kind != "DaemonSet" {
			continue
		}
		if c != model.DaemonSetSensor && kind == "DaemonSet" {
			continue
		}
		return c, true
	}
	return "", false
}

// ComponentForPod classifies a running pod using its owner kind
// (ReplicaSet counts as Deployment).
func ComponentForPod(p *corev1.Pod) (model.Component, bool) {
	kind := "Deployment"
	for _, o := range p.OwnerReferences {
		if o.Kind == "DaemonSet" {
			kind = "DaemonSet"
		}
	}
	return Classify(kind, images(p.Spec), p.Labels)
}

func images(s corev1.PodSpec) []string {
	var out []string
	for _, c := range s.Containers {
		out = append(out, c.Image)
	}
	return out
}

// Discover lists workloads in the given namespaces (nil = all namespaces).
func Discover(ctx context.Context, apps appsv1client.AppsV1Interface, namespaces []string) (Result, error) {
	if len(namespaces) == 0 {
		namespaces = []string{metav1.NamespaceAll}
	}
	var res Result
	for _, ns := range namespaces {
		opts := metav1.ListOptions{Limit: 200}
		for {
			ds, err := apps.DaemonSets(ns).List(ctx, opts)
			if err != nil {
				return res, err
			}
			for _, d := range ds.Items {
				if c, ok := Classify("DaemonSet", images(d.Spec.Template.Spec), d.Spec.Template.Labels); ok {
					res.Workloads = append(res.Workloads, Workload{c, d.Namespace, "DaemonSet", d.Name})
				}
			}
			if ds.Continue == "" {
				break
			}
			opts.Continue = ds.Continue
		}
		opts = metav1.ListOptions{Limit: 200}
		for {
			dep, err := apps.Deployments(ns).List(ctx, opts)
			if err != nil {
				return res, err
			}
			for _, d := range dep.Items {
				if c, ok := Classify("Deployment", images(d.Spec.Template.Spec), d.Spec.Template.Labels); ok {
					res.Workloads = append(res.Workloads, Workload{c, d.Namespace, "Deployment", d.Name})
				}
			}
			if dep.Continue == "" {
				break
			}
			opts.Continue = dep.Continue
		}
	}
	sort.Slice(res.Workloads, func(i, j int) bool {
		a, b := res.Workloads[i], res.Workloads[j]
		if a.Namespace != b.Namespace {
			return a.Namespace < b.Namespace
		}
		return a.Name < b.Name
	})
	seen := map[string]bool{}
	for _, w := range res.Workloads {
		if !seen[w.Namespace] {
			seen[w.Namespace] = true
			res.Namespaces = append(res.Namespaces, w.Namespace)
		}
	}
	return res, nil
}
