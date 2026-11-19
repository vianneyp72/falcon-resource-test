package discover

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/vianneyp72/falcon-cloud-security-labs/tools/falcon-footprint/internal/model"
)

func podSpec(images ...string) corev1.PodTemplateSpec {
	var cs []corev1.Container
	for _, im := range images {
		cs = append(cs, corev1.Container{Name: "c", Image: im})
	}
	return corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: cs}}
}

func TestClassify(t *testing.T) {
	cases := []struct {
		kind, image string
		labels      map[string]string
		want        model.Component
		ok          bool
	}{
		{"DaemonSet", "registry.crowdstrike.com/falcon-sensor/us-1/release/falcon-sensor:7.20.0-17306-1.falcon-linux.Release.US-1", nil, model.DaemonSetSensor, true},
		{"DaemonSet", "123.dkr.ecr.us-east-1.amazonaws.com/falcon-sensor@sha256:abc", nil, model.DaemonSetSensor, true},
		{"Deployment", "registry.crowdstrike.com/falcon-kac/us-1/release/falcon-kac:7.39.0-3702", nil, model.KAC, true},
		{"Deployment", "myacr.azurecr.io/falcon-imageanalyzer:1.0.25", nil, model.IAR, true},
		{"Deployment", "myregistry/falcon-container:7.39.0-7802", nil, model.Injector, true},
		{"Deployment", "quay.io/crowdstrike/falcon-operator:1.3.0", nil, model.Operator, true},
		{"Deployment", "mirror/custom-name:1", map[string]string{"app.kubernetes.io/name": "falcon-kac"}, model.KAC, true},
		{"Deployment", "nginx:1.27", nil, "", false},
		// falcon-container inside a DaemonSet is not the injector
		{"DaemonSet", "myregistry/falcon-container:7", nil, "", false},
	}
	for _, c := range cases {
		got, ok := Classify(c.kind, []string{c.image}, c.labels)
		if got != c.want || ok != c.ok {
			t.Errorf("Classify(%s, %s) = %q,%v want %q,%v", c.kind, c.image, got, ok, c.want, c.ok)
		}
	}
}

func TestImageName(t *testing.T) {
	for in, want := range map[string]string{
		"falcon-sensor":                      "falcon-sensor",
		"a/b/falcon-sensor:1.2":              "falcon-sensor",
		"host:5000/falcon-kac@sha256:deadbe": "falcon-kac",
		"host:5000/falcon-kac:7.1@sha256:de": "falcon-kac",
	} {
		if got := ImageName(in); got != want {
			t.Errorf("ImageName(%q)=%q want %q", in, got, want)
		}
	}
}

func TestDiscover(t *testing.T) {
	cs := fake.NewSimpleClientset(
		&appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Namespace: "falcon-system", Name: "falcon-sensor"},
			Spec: appsv1.DaemonSetSpec{Template: podSpec("r/falcon-sensor:7")}},
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "falcon-kac", Name: "falcon-kac"},
			Spec: appsv1.DeploymentSpec{Template: podSpec("r/falcon-kac:7", "r/falcon-kac:7")}},
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "web"},
			Spec: appsv1.DeploymentSpec{Template: podSpec("nginx")}},
	)
	res, err := Discover(context.Background(), cs.AppsV1(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Workloads) != 2 {
		t.Fatalf("want 2 workloads, got %+v", res.Workloads)
	}
	if len(res.Namespaces) != 2 || res.Namespaces[0] != "falcon-kac" || res.Namespaces[1] != "falcon-system" {
		t.Fatalf("namespaces sorted+deduped: %v", res.Namespaces)
	}
	if res.Workloads[0].Component != model.KAC {
		t.Fatalf("workloads sorted by namespace: %+v", res.Workloads)
	}

	// override limits the search
	res, _ = Discover(context.Background(), cs.AppsV1(), []string{"falcon-system"})
	if len(res.Workloads) != 1 || res.Workloads[0].Kind != "DaemonSet" {
		t.Fatalf("override: %+v", res.Workloads)
	}
}

func TestComponentForPod(t *testing.T) {
	p := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{OwnerReferences: []metav1.OwnerReference{{Kind: "DaemonSet"}}},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Image: "r/falcon-sensor:7"}}},
	}
	if c, ok := ComponentForPod(p); !ok || c != model.DaemonSetSensor {
		t.Fatalf("sensor pod: %q %v", c, ok)
	}
	p = &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet"}}},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Image: "r/falcon-container:7"}}},
	}
	if c, ok := ComponentForPod(p); !ok || c != model.Injector {
		t.Fatalf("injector pod: %q %v", c, ok)
	}
	p = &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{{Image: "busybox"}}}}
	if _, ok := ComponentForPod(p); ok {
		t.Fatal("helm test pod should not classify")
	}
}
