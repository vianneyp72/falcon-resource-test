# Part 7: Packaging (Tasks 23-27)

This part turns the binary into what a customer installs: an image, a Helm chart, a one-line script, and CI that keeps the tool honest about its own footprint.

**Facts from earlier parts:**
- The binary is `bin/falcon-resource-test`, built with `CGO_ENABLED=0` (modernc SQLite is pure Go). It listens on `:8080` and writes to `DATA_DIR=/data`.
- Env vars come from Task 16, Task 17 (`FALCON_API_DIR`, `FALCON_CLOUD`), and Task 18 (`PROMETHEUS_URL`, `PROMETHEUS_BACKFILL_DAYS`).
- RBAC must match `setup.Required` (Task 14) exactly. That's 9 `list` permissions plus `create` on `selfsubjectaccessreviews`, with no write verbs on anything else and no Secrets access.
- Discovery accepts `app.kubernetes.io/name` on the pod template as well as the image name (Task 5). The e2e stand-ins rely on that.

---

### Task 23: Dockerfile

**Files:**
- Create: `Dockerfile`, `.dockerignore`

The Dockerfile builds everything from source (UI, then Go), so `docker build .` is the complete build-your-own path. The final stage is distroless `static:nonroot`: no shell, no package manager, UID 65532.

- [ ] **Step 1: `.dockerignore`**

```
bin/
ui/node_modules/
internal/web/dist/*
!internal/web/dist/.gitkeep
**/*_test.go
test/
chart/
```

- [ ] **Step 2: `Dockerfile`**

```dockerfile
# syntax=docker/dockerfile:1.7
# UI is platform-independent; build it once on the build host.
FROM --platform=$BUILDPLATFORM node:22-alpine AS ui
WORKDIR /src/ui
COPY ui/package.json ui/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY ui/ ./
RUN mkdir -p ../internal/web/dist && npm run build

# Cross-compile Go on the build host for each target arch (no QEMU for Go).
FROM --platform=$BUILDPLATFORM golang:1.24-alpine AS go
ARG TARGETOS TARGETARCH VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ cmd/
COPY internal/ internal/
COPY --from=ui /src/internal/web/dist/ internal/web/dist/
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/falcon-resource-test ./cmd/falcon-resource-test

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=go /out/falcon-resource-test /falcon-resource-test
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/falcon-resource-test"]
```

> **Note:** `npm ci` inside the image uses the public registry URLs in `package-lock.json`. That's why `npm run fix-lockfile` has to run before every lockfile commit (Task 19).

- [ ] **Step 3: Build and check the size budget**

```bash
make image
docker buildx build --load --build-arg VERSION=dev -t falcon-resource-test:dev .
docker save falcon-resource-test:dev | gzip | wc -c
```

Expected: under `26214400` (25 MiB, measured compressed per index deviation 3). A typical result is about 12 MB: distroless static is about 2 MB, plus the binary.

The `--load` build is single-arch, because `docker save` can't export a multi-arch index. `make image` checks that both architectures build.

- [ ] **Step 4: Smoke-run outside a cluster**

```bash
docker run --rm falcon-resource-test:dev; echo "exit=$?"
```

Expected: a JSON log line `"msg":"fatal"` mentioning `KUBERNETES_SERVICE_HOST`, and `exit=1`. The binary starts, parses config, and fails cleanly without in-cluster credentials.

- [ ] **Step 5: Commit**

```bash
git add Dockerfile .dockerignore
git commit -m "falcon-resource-test: distroless multi-arch Dockerfile

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 24: Helm chart

**Files:**
- Create: `chart/Chart.yaml`, `chart/values.yaml`, `chart/.helmignore`
- Create: `chart/templates/_helpers.tpl`, `serviceaccount.yaml`, `rbac.yaml`, `deployment.yaml`, `service.yaml`, `pvc.yaml`, `networkpolicy.yaml`, `ingress.yaml`, `NOTES.txt`
- Test: `test/chart.sh`

The chart is the one install artifact. A GitOps engineer reading `values.yaml` should see every knob with a one-line comment, and should find nothing surprising in `helm template`.

- [ ] **Step 1: `chart/Chart.yaml`**

```yaml
apiVersion: v2
name: falcon-resource-test
description: Read-only, in-cluster view of the CPU, memory, and health cost of CrowdStrike Falcon, with exportable assessment reports.
type: application
version: 0.1.0
appVersion: "0.1.0"
kubeVersion: ">=1.25.0-0"
home: https://github.com/vianneyp72/falcon-resource-test
sources:
  - https://github.com/vianneyp72/falcon-resource-test
keywords: [crowdstrike, falcon, resource-usage, observability]
```

`kubeVersion >=1.25` is required because Pod Security Admission and `seccompProfile` in `securityContext` are both GA from 1.25.

- [ ] **Step 2: `chart/values.yaml`**

```yaml
# Falcon Resource Test. Every value is optional; the defaults are a complete,
# read-only install that keeps 24h of history in memory-backed storage.

image:
  repository: ghcr.io/vianneyp72/falcon-resource-test
  tag: ""                  # defaults to the chart appVersion
  pullPolicy: IfNotPresent
imagePullSecrets: []       # for a private mirror: [{name: my-pull-secret}]

# Shown in the UI and the exported report's file name.
clusterName: ""            # defaults to "kubernetes"; set it to the name your team uses

collection:
  interval: 15s            # how often pod and node metrics are sampled (min 5s)

discovery:
  namespaces: []           # empty = find Falcon in every namespace; or e.g. [falcon-system, falcon-kac]

storage:
  sizeLimit: 256Mi         # emptyDir size when persistence is off (about 24h of history)

persistence:
  enabled: false           # keep history across restarts on a PVC
  size: 1Gi
  storageClass: ""         # "" = cluster default
  retentionDays: 30

# Optional: sensor version and online state per node from the Falcon API.
# Needs an API client with Hosts: Read. Create the Secret yourself:
#   kubectl -n falcon-resource-test create secret generic falcon-api \
#     --from-literal=client_id=$FALCON_CLIENT_ID --from-literal=client_secret=$FALCON_CLIENT_SECRET
falconApi:
  enabled: false
  existingSecret: ""       # Secret name with keys client_id and client_secret
  cloud: autodiscover      # autodiscover | us-1 | us-2 | eu-1 | us-gov-1 | us-gov-2 (Gov must be explicit)

# Optional: import past Falcon usage from an existing Prometheus (cAdvisor metrics).
prometheus:
  url: ""                  # e.g. http://prometheus-server.monitoring.svc:80
  backfillDays: 7

networkPolicy:
  enabled: true
  # Egress to the Kubernetes API server. Empty = any destination on the ports
  # below. Set your API server CIDR(s) to lock it down further.
  apiServerCIDRs: []
  apiServerPorts: [443, 6443]

ingress:
  enabled: false           # the default access path is kubectl port-forward
  className: ""
  host: ""
  annotations: {}
  tls: []

service:
  port: 80

resources:
  requests: { cpu: 10m, memory: 32Mi }
  limits:   { cpu: 100m, memory: 128Mi }

nodeSelector: {}
tolerations: []
affinity: {}
podAnnotations: {}
podLabels: {}

# e2e tests only: extra assessment durations, e.g. "1m".
extraScanDurations: ""
```

`clusterName` can't default to the kube context name, because that's a client-side setting the pod never sees.

- [ ] **Step 3: `chart/templates/_helpers.tpl`**

```
{{- define "frt.name" -}}falcon-resource-test{{- end -}}

{{- define "frt.fullname" -}}
{{- if contains "falcon-resource-test" .Release.Name -}}{{ .Release.Name | trunc 63 }}{{- else -}}{{ printf "%s-falcon-resource-test" .Release.Name | trunc 63 | trimSuffix "-" }}{{- end -}}
{{- end -}}

{{- define "frt.selectorLabels" -}}
app.kubernetes.io/name: falcon-resource-test
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "frt.labels" -}}
{{ include "frt.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version }}
{{- end -}}

{{/* Converts a Kubernetes quantity in Mi or Gi to bytes, for DISK_LIMIT_BYTES. */}}
{{- define "frt.bytes" -}}
{{- $v := toString . -}}
{{- if hasSuffix "Gi" $v -}}{{ mul (trimSuffix "Gi" $v | int64) 1073741824 }}
{{- else if hasSuffix "Mi" $v -}}{{ mul (trimSuffix "Mi" $v | int64) 1048576 }}
{{- else -}}{{ fail (printf "size %q must be in Mi or Gi, e.g. 256Mi or 1Gi" $v) }}
{{- end -}}
{{- end -}}
```

The selector label is `app.kubernetes.io/name: falcon-resource-test`. That name isn't in discovery's `byName` map (Task 5), so the tool never mistakes itself for a Falcon component.

- [ ] **Step 4: Write the failing RBAC test**

The ClusterRole has to grant exactly what `setup.Required` checks for. This test reads the chart's `rbac.yaml` and fails if any of these hold:
- a required permission is missing,
- the role grants anything beyond those plus SelfSubjectAccessReview `create`,
- Secrets appear anywhere.

So the Setup page and the chart can't drift apart.

`internal/setup/rbac_test.go`:

```go
package setup

import (
	"os"
	"strings"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	"sigs.k8s.io/yaml"
)

// chartRole parses the ClusterRole out of chart/templates/rbac.yaml. Template
// lines ({{ }}) only appear in metadata, so dropping them leaves valid YAML.
func chartRole(t *testing.T) rbacv1.ClusterRole {
	raw, err := os.ReadFile("../../chart/templates/rbac.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, doc := range strings.Split(string(raw), "\n---") {
		var kept []string
		for _, l := range strings.Split(doc, "\n") {
			if !strings.Contains(l, "{{") {
				kept = append(kept, l)
			}
		}
		var r rbacv1.ClusterRole
		if err := yaml.Unmarshal([]byte(strings.Join(kept, "\n")), &r); err != nil {
			t.Fatalf("parse rbac.yaml: %v", err)
		}
		if r.Kind == "ClusterRole" {
			return r
		}
	}
	t.Fatal("no ClusterRole in rbac.yaml")
	return rbacv1.ClusterRole{}
}

func TestChartRBACMatchesRequired(t *testing.T) {
	granted := map[string]bool{}
	for _, rule := range chartRole(t).Rules {
		for _, g := range rule.APIGroups {
			for _, res := range rule.Resources {
				for _, v := range rule.Verbs {
					if res == "secrets" || res == "*" || v == "*" {
						t.Fatalf("forbidden grant: %s/%s %s", g, res, v)
					}
					granted[g+"/"+res+"/"+v] = true
				}
			}
		}
	}
	want := map[string]bool{"authorization.k8s.io/selfsubjectaccessreviews/create": true}
	for _, p := range Required {
		want[p.Group+"/"+p.Resource+"/"+p.Verb] = true
	}
	for k := range want {
		if !granted[k] {
			t.Errorf("chart is missing %s (setup.Required checks for it)", k)
		}
	}
	for k := range granted {
		if !want[k] {
			t.Errorf("chart grants %s, which nothing needs", k)
		}
	}
}
```

Run: `go test ./internal/setup/ -run TestChartRBAC`
Expected: FAIL, `open ../../chart/templates/rbac.yaml: no such file or directory`.

> **Note:** `sigs.k8s.io/yaml` is already in `go.sum` as a client-go dependency. If `go vet` reports it as indirect, run `go mod tidy`.

- [ ] **Step 5: `chart/templates/serviceaccount.yaml` and `rbac.yaml`**

```yaml
apiVersion: v1
kind: ServiceAccount
metadata:
  name: {{ include "frt.fullname" . }}
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "frt.labels" . | nindent 4 }}
automountServiceAccountToken: true
```

`rbac.yaml`. A ClusterRole is needed because discovery looks for Falcon in every namespace. Every verb is `list`, except the one `create` that any authenticated user already has:

```yaml
# Read-only. Mirrors internal/setup.Required; TestChartRBACMatchesRequired
# fails the build if the two drift. No Secrets, no exec, no write verbs.
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: {{ include "frt.fullname" . }}
  labels:
    {{- include "frt.labels" . | nindent 4 }}
rules:
  - apiGroups: [""]
    resources: [pods, nodes, events]
    verbs: [list]
  - apiGroups: [apps]
    resources: [daemonsets, deployments]
    verbs: [list]
  - apiGroups: [admissionregistration.k8s.io]
    resources: [mutatingwebhookconfigurations, validatingwebhookconfigurations]
    verbs: [list]
  - apiGroups: [metrics.k8s.io]
    resources: [pods, nodes]
    verbs: [list]
  # Setup page permission checks. Every authenticated user has this by default.
  - apiGroups: [authorization.k8s.io]
    resources: [selfsubjectaccessreviews]
    verbs: [create]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: {{ include "frt.fullname" . }}
  labels:
    {{- include "frt.labels" . | nindent 4 }}
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: {{ include "frt.fullname" . }}
subjects:
  - kind: ServiceAccount
    name: {{ include "frt.fullname" . }}
    namespace: {{ .Release.Namespace }}
```

Run: `go test ./internal/setup/ -run TestChartRBAC`
Expected: PASS.

> **Note:** The spec listed `get/list/watch` and `namespaces`. Polling (index deviation 1) only uses `list`, and discovery never lists namespaces, so the chart grants less than the spec allowed. That's intentional.

- [ ] **Step 6: `chart/templates/deployment.yaml`**

```yaml
{{- $size := ternary .Values.persistence.size .Values.storage.sizeLimit .Values.persistence.enabled -}}
apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ include "frt.fullname" . }}
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "frt.labels" . | nindent 4 }}
spec:
  replicas: 1                 # single writer to SQLite; never scale this up
  strategy:
    type: Recreate            # a PVC is ReadWriteOnce; two pods can't share it
  selector:
    matchLabels:
      {{- include "frt.selectorLabels" . | nindent 6 }}
  template:
    metadata:
      labels:
        {{- include "frt.selectorLabels" . | nindent 8 }}
        {{- with .Values.podLabels }}{{ toYaml . | nindent 8 }}{{ end }}
      {{- with .Values.podAnnotations }}
      annotations:
        {{- toYaml . | nindent 8 }}
      {{- end }}
    spec:
      serviceAccountName: {{ include "frt.fullname" . }}
      {{- with .Values.imagePullSecrets }}
      imagePullSecrets:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      securityContext:
        runAsNonRoot: true
        runAsUser: 65532
        runAsGroup: 65532
        fsGroup: 65532
        seccompProfile:
          type: RuntimeDefault
      containers:
        - name: falcon-resource-test
          image: "{{ .Values.image.repository }}:{{ .Values.image.tag | default .Chart.AppVersion }}"
          imagePullPolicy: {{ .Values.image.pullPolicy }}
          ports:
            - name: http
              containerPort: 8080
          env:
            - name: CLUSTER_NAME
              value: {{ .Values.clusterName | default "kubernetes" | quote }}
            - name: COLLECTION_INTERVAL
              value: {{ .Values.collection.interval | quote }}
            - name: DISCOVERY_NAMESPACES
              value: {{ join "," .Values.discovery.namespaces | quote }}
            - name: PERSISTENT
              value: {{ .Values.persistence.enabled | quote }}
            - name: RETENTION_DAYS
              value: {{ .Values.persistence.retentionDays | quote }}
            - name: DISK_LIMIT_BYTES
              value: {{ include "frt.bytes" $size | quote }}
            {{- with .Values.extraScanDurations }}
            - name: EXTRA_SCAN_DURATIONS
              value: {{ . | quote }}
            {{- end }}
            {{- if .Values.falconApi.enabled }}
            - name: FALCON_API_DIR
              value: /etc/falcon-api
            - name: FALCON_CLOUD
              value: {{ .Values.falconApi.cloud | quote }}
            {{- end }}
            {{- with .Values.prometheus.url }}
            - name: PROMETHEUS_URL
              value: {{ . | quote }}
            - name: PROMETHEUS_BACKFILL_DAYS
              value: {{ $.Values.prometheus.backfillDays | quote }}
            {{- end }}
            - name: POD_NAMESPACE
              valueFrom: { fieldRef: { fieldPath: metadata.namespace } }
            - name: POD_NAME
              valueFrom: { fieldRef: { fieldPath: metadata.name } }
            - name: MEMORY_LIMIT_BYTES
              valueFrom: { resourceFieldRef: { containerName: falcon-resource-test, resource: limits.memory } }
          readinessProbe:
            httpGet: { path: /readyz, port: http }
            periodSeconds: 10
          livenessProbe:
            httpGet: { path: /healthz, port: http }
            initialDelaySeconds: 10
            periodSeconds: 30
          resources:
            {{- toYaml .Values.resources | nindent 12 }}
          securityContext:
            allowPrivilegeEscalation: false
            readOnlyRootFilesystem: true
            capabilities:
              drop: [ALL]
          volumeMounts:
            - name: data
              mountPath: /data
            {{- if .Values.falconApi.enabled }}
            - name: falcon-api
              mountPath: /etc/falcon-api
              readOnly: true
            {{- end }}
      volumes:
        - name: data
          {{- if .Values.persistence.enabled }}
          persistentVolumeClaim:
            claimName: {{ include "frt.fullname" . }}
          {{- else }}
          emptyDir:
            sizeLimit: {{ .Values.storage.sizeLimit }}
          {{- end }}
        {{- if .Values.falconApi.enabled }}
        - name: falcon-api
          secret:
            secretName: {{ required "falconApi.existingSecret is required when falconApi.enabled=true" .Values.falconApi.existingSecret }}
            defaultMode: 0400
        {{- end }}
      {{- with .Values.nodeSelector }}
      nodeSelector:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      {{- with .Values.tolerations }}
      tolerations:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      {{- with .Values.affinity }}
      affinity:
        {{- toYaml . | nindent 8 }}
      {{- end }}
```

> **Note:** Mounting the Falcon API Secret as a volume is the kubelet's job. The tool's ServiceAccount still has no Secrets permission, and `TestChartRBACMatchesRequired` keeps it that way.

> **Note:** The emptyDir is disk-backed (no `medium: Memory`), so stored history doesn't count against the 128Mi memory limit.

- [ ] **Step 7: `service.yaml`, `pvc.yaml`, `ingress.yaml`**

`service.yaml`:

```yaml
apiVersion: v1
kind: Service
metadata:
  name: {{ include "frt.fullname" . }}
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "frt.labels" . | nindent 4 }}
spec:
  type: ClusterIP
  selector:
    {{- include "frt.selectorLabels" . | nindent 4 }}
  ports:
    - name: http
      port: {{ .Values.service.port }}
      targetPort: http
```

`pvc.yaml`:

```yaml
{{- if .Values.persistence.enabled }}
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: {{ include "frt.fullname" . }}
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "frt.labels" . | nindent 4 }}
spec:
  accessModes: [ReadWriteOnce]
  {{- with .Values.persistence.storageClass }}
  storageClassName: {{ . }}
  {{- end }}
  resources:
    requests:
      storage: {{ .Values.persistence.size }}
{{- end }}
```

> **Note:** The PVC has no `helm.sh/resource-policy: keep`. The spec promises `helm uninstall` leaves nothing behind, so history is deleted with the release. Export any reports you want to keep before you uninstall.

`ingress.yaml`:

```yaml
{{- if .Values.ingress.enabled }}
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: {{ include "frt.fullname" . }}
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "frt.labels" . | nindent 4 }}
  {{- with .Values.ingress.annotations }}
  annotations:
    {{- toYaml . | nindent 4 }}
  {{- end }}
spec:
  {{- with .Values.ingress.className }}
  ingressClassName: {{ . }}
  {{- end }}
  {{- with .Values.ingress.tls }}
  tls:
    {{- toYaml . | nindent 4 }}
  {{- end }}
  rules:
    - host: {{ required "ingress.host is required when ingress.enabled=true" .Values.ingress.host | quote }}
      http:
        paths:
          - path: /
            pathType: Prefix
            backend:
              service:
                name: {{ include "frt.fullname" . }}
                port:
                  name: http
{{- end }}
```

> **Warning:** The UI has no login. An Ingress exposes Falcon usage data to anyone who can reach the host, so put it behind your existing SSO or auth proxy annotations. That's why it's off by default.

- [ ] **Step 8: `networkpolicy.yaml`**

The tool only needs four kinds of traffic: inbound UI/API on 8080, DNS, the Kubernetes API, and (only when enabled) the Falcon API and Prometheus. Everything else is denied.

```yaml
{{- if .Values.networkPolicy.enabled }}
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: {{ include "frt.fullname" . }}
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "frt.labels" . | nindent 4 }}
spec:
  podSelector:
    matchLabels:
      {{- include "frt.selectorLabels" . | nindent 6 }}
  policyTypes: [Ingress, Egress]
  ingress:
    - ports:
        - port: 8080
          protocol: TCP
  egress:
    # DNS
    - ports:
        - { port: 53, protocol: UDP }
        - { port: 53, protocol: TCP }
    # Kubernetes API server
    - ports:
        {{- range .Values.networkPolicy.apiServerPorts }}
        - { port: {{ . }}, protocol: TCP }
        {{- end }}
      {{- with .Values.networkPolicy.apiServerCIDRs }}
      to:
        {{- range . }}
        - ipBlock: { cidr: {{ . }} }
        {{- end }}
      {{- end }}
    {{- if .Values.falconApi.enabled }}
    # Falcon API (api.*.crowdstrike.com); HTTPS only
    - ports:
        - { port: 443, protocol: TCP }
    {{- end }}
    {{- with .Values.prometheus.url }}
    {{- $u := urlParse . }}
    {{- $port := ternary "443" "80" (eq $u.scheme "https") }}
    {{- if contains ":" $u.host }}{{ $port = last (splitList ":" $u.host) }}{{ end }}
    # Prometheus backfill
    - ports:
        - { port: {{ $port }}, protocol: TCP }
    {{- end }}
{{- end }}
```

> **Note:** API server egress defaults to "any destination on 443/6443". That's because the API server's IP differs per cluster (EKS/GKE/AKS endpoints aren't pod IPs), so a hard-coded CIDR would break the install. The values comment tells operators how to pin `apiServerCIDRs`.

- [ ] **Step 9: `NOTES.txt` and `.helmignore`**

`chart/templates/NOTES.txt`:

```
falcon-resource-test is starting. It is read-only: it lists pods, nodes, events,
DaemonSets, Deployments, webhooks and metrics. It cannot read Secrets or change anything.

Open the UI:

  kubectl -n {{ .Release.Namespace }} port-forward svc/{{ include "frt.fullname" . }} 8080:{{ .Values.service.port }}
  # then browse to http://localhost:8080

{{- if not .Values.persistence.enabled }}

History is kept on an emptyDir ({{ .Values.storage.sizeLimit }}) and is lost when the pod restarts.
Export reports you want to keep, or set persistence.enabled=true.
{{- end }}

Remove it:

  helm -n {{ .Release.Namespace }} uninstall {{ .Release.Name }}
```

`chart/.helmignore`:

```
.DS_Store
*.tgz
*.orig
*~
.git/
```

- [ ] **Step 10: `test/chart.sh`**

```bash
#!/usr/bin/env bash
# Lints and renders the chart in every supported mode and checks the
# guarantees we make to security reviewers.
set -euo pipefail
cd "$(dirname "$0")/.."
CHART=chart

render() { helm template ff "$CHART" --namespace falcon-resource-test "$@"; }
fail() { echo "FAIL: $*" >&2; exit 1; }

helm lint --strict "$CHART"

DEFAULT=$(render)
PERSIST=$(render --set persistence.enabled=true)
API=$(render --set falconApi.enabled=true --set falconApi.existingSecret=falcon-api-creds \
             --set prometheus.url=http://prometheus-server.monitoring.svc:9090)

# Read-only RBAC: no Secrets, no write verbs anywhere.
for out in "$DEFAULT" "$PERSIST" "$API"; do
  echo "$out" | grep -qE 'resources:.*\bsecrets\b' && fail "RBAC mentions secrets"
  echo "$out" | grep -E '^\s*verbs:' | grep -vE 'verbs: \[(list|create)\]$' \
    && fail "unexpected RBAC verbs"
done

# Restricted Pod Security.
echo "$DEFAULT" | grep -q 'readOnlyRootFilesystem: true' || fail "rootfs not read-only"
echo "$DEFAULT" | grep -q 'runAsNonRoot: true'           || fail "not runAsNonRoot"
echo "$DEFAULT" | grep -q 'type: RuntimeDefault'         || fail "no seccomp profile"

# Storage mode.
echo "$DEFAULT" | grep -q 'emptyDir:'                    || fail "default should use emptyDir"
echo "$DEFAULT" | grep -q 'kind: PersistentVolumeClaim'  && fail "default should not create a PVC"
echo "$PERSIST" | grep -q 'kind: PersistentVolumeClaim'  || fail "persistence should create a PVC"
echo "$DEFAULT" | grep -A1 'name: DISK_LIMIT_BYTES' | grep -q '"268435456"' || fail "256Mi -> bytes"

# Optional integrations render their wiring only when on.
echo "$DEFAULT" | grep -q 'FALCON_API_DIR'               && fail "falcon api env without opt-in"
echo "$API"     | grep -q 'secretName: falcon-api-creds' || fail "falcon api secret volume"
echo "$API"     | grep -q 'port: 9090'                   || fail "prometheus egress port"

# Misconfigurations fail fast.
render --set falconApi.enabled=true >/dev/null 2>&1 && fail "falconApi without existingSecret should fail"
render --set ingress.enabled=true   >/dev/null 2>&1 && fail "ingress without host should fail"

# Schema check, when kubeconform is installed (CI installs it).
if command -v kubeconform >/dev/null; then
  for out in "$DEFAULT" "$PERSIST" "$API"; do
    echo "$out" | kubeconform -strict -summary -kubernetes-version 1.25.0
  done
fi

echo "chart OK"
```

Make it executable and add a Makefile target:

```bash
chmod +x test/chart.sh
```

```makefile
.PHONY: test ui build image e2e chart-test
chart-test:
	./test/chart.sh
```

(Replace the existing `.PHONY` line rather than adding a second one.)

- [ ] **Step 11: Run the chart checks and the RBAC test**

Run: `./test/chart.sh && go test ./internal/setup/ -run TestChartRBACMatchesRequired -v`
Expected: `chart OK`, then `--- PASS: TestChartRBACMatchesRequired`.

If `unexpected RBAC verbs` fires, check that `rbac.yaml` writes verbs and resources in flow style on one line (`verbs: [list]`); the greps rely on it.

- [ ] **Step 12: Commit**

```bash
git add chart test/chart.sh internal/setup/rbac_test.go Makefile
git commit -m "falcon-resource-test: read-only Helm chart

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 25: `scripts/assess.sh`

The one-command path for an SE on a customer call. It is a thin wrapper around `helm`. Everything it does, a GitOps user can reproduce from the chart alone.

**Files:**
- Create: `scripts/assess.sh`

- [ ] **Step 1: Write the script**

```bash
#!/usr/bin/env bash
# Installs falcon-resource-test, waits for it, and opens the UI through a port-forward.
#   ./scripts/assess.sh                       # published chart, namespace falcon-resource-test
#   ./scripts/assess.sh --set clusterName=prod-east
#   CHART=./chart ./scripts/assess.sh         # local checkout
# Extra arguments are passed to helm unchanged.
set -euo pipefail

NAMESPACE=${NAMESPACE:-falcon-resource-test}
RELEASE=${RELEASE:-falcon-resource-test}
CHART=${CHART:-oci://ghcr.io/vianneyp72/charts/falcon-resource-test}
LOCAL_PORT=${LOCAL_PORT:-8080}

need() { command -v "$1" >/dev/null || { echo "error: $1 is required but not installed" >&2; exit 1; }; }
need kubectl
need helm

CONTEXT=$(kubectl config current-context 2>/dev/null) \
  || { echo "error: no current kubectl context; run your cloud's get-credentials command first" >&2; exit 1; }
echo "Cluster context: $CONTEXT"
echo "Namespace:       $NAMESPACE"

helm upgrade --install "$RELEASE" "$CHART" \
  --namespace "$NAMESPACE" --create-namespace \
  --wait --timeout 3m "$@"

kubectl -n "$NAMESPACE" rollout status deploy/"$RELEASE" --timeout 2m

URL="http://localhost:$LOCAL_PORT"
echo
echo "falcon-resource-test is running. Opening $URL"
echo "Press Ctrl+C to stop the port-forward. The tool keeps collecting in the cluster."
echo
echo "When you are done:"
echo "  helm -n $NAMESPACE uninstall $RELEASE && kubectl delete namespace $NAMESPACE"
echo

( sleep 2
  if command -v open >/dev/null; then open "$URL"
  elif command -v xdg-open >/dev/null; then xdg-open "$URL" >/dev/null 2>&1
  elif command -v cmd.exe >/dev/null; then cmd.exe /c start "$URL"
  fi ) &

exec kubectl -n "$NAMESPACE" port-forward svc/"$RELEASE" "$LOCAL_PORT":80
```

> **Note:** The release is named `falcon-resource-test`, so `frt.fullname` resolves to `falcon-resource-test` (the release name already contains it). The `deploy/` and `svc/` names in the script depend on that. If you change `RELEASE`, use a name that contains `falcon-resource-test`, or both lookups will miss.

- [ ] **Step 2: Lint it**

Run: `chmod +x scripts/assess.sh && bash -n scripts/assess.sh && (command -v shellcheck >/dev/null && shellcheck scripts/assess.sh || true)`
Expected: no output.

- [ ] **Step 3: Try it against kind with the local chart**

```bash
kind create cluster --name ff-dev
docker build -t falcon-resource-test:dev . && kind load docker-image falcon-resource-test:dev --name ff-dev
CHART=./chart ./scripts/assess.sh --set image.repository=falcon-resource-test --set image.tag=dev
```

Expected: helm reports `STATUS: deployed`, the rollout finishes, and the browser opens to the Live page. Without Falcon installed, it shows the "No Falcon components found" banner. Press Ctrl+C, then run the uninstall line the script printed.

> **Note:** This uses plain `docker build` instead of `make image`. `make image` builds multi-arch with buildx, which doesn't load the result into the local Docker store that `kind load` reads from.

- [ ] **Step 4: Commit**

```bash
git add scripts/assess.sh
git commit -m "falcon-resource-test: one-command assess.sh

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 26: kind e2e + budget gate

This is the spec's integration test and resource budget gate. It runs the real image and chart in kind, with metrics-server and stand-in Falcon workloads. It then checks the API, runs a 1-minute assessment, exports it, and finally measures the tool's own CPU and memory.

**Files:**
- Create: `test/fake-falcon.yaml`
- Create: `test/e2e.sh`

- [ ] **Step 1: Stand-in Falcon workloads**

These are `pause` containers labeled the way discovery expects (Task 5: the `app.kubernetes.io/name` label matches `byName`). The DaemonSet Sensor stand-in must be a DaemonSet and KAC must be a Deployment, because `Classify` checks the workload kind.

`test/fake-falcon.yaml`:

```yaml
apiVersion: v1
kind: Namespace
metadata: { name: falcon-system }
---
apiVersion: v1
kind: Namespace
metadata: { name: falcon-kac }
---
apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: falcon-node-sensor
  namespace: falcon-system
spec:
  selector:
    matchLabels: { app.kubernetes.io/name: falcon-sensor }
  template:
    metadata:
      labels: { app.kubernetes.io/name: falcon-sensor }
    spec:
      tolerations: [{ operator: Exists }]
      containers:
        - name: falcon-node-sensor
          image: registry.k8s.io/pause:3.10
          resources:
            requests: { cpu: 5m, memory: 8Mi }
            limits: { memory: 32Mi }
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: falcon-kac
  namespace: falcon-kac
spec:
  replicas: 1
  selector:
    matchLabels: { app.kubernetes.io/name: falcon-kac }
  template:
    metadata:
      labels: { app.kubernetes.io/name: falcon-kac }
    spec:
      containers:
        - name: falcon-kac
          image: registry.k8s.io/pause:3.10
          resources:
            requests: { cpu: 5m, memory: 8Mi }
            limits: { memory: 32Mi }
```

> **Note:** The stand-ins use `pause` and have no webhook or Falcon API, so checks like webhook failure policy and sensor version come back `not_measured` or `info`. That's expected. The e2e test checks wiring, not Falcon behavior. The spec's "Live validation (before release)" step on a real Falcon cluster covers that, and is a manual release checklist item in Task 27.

- [ ] **Step 2: Write `test/e2e.sh`**

```bash
#!/usr/bin/env bash
# End-to-end test in kind: install, API checks, a 1m assessment, export, and
# the resource budget gate. Needs docker, kind, kubectl, helm, jq, curl.
#   KEEP=1 ./test/e2e.sh    # leave the cluster up afterwards
set -euo pipefail
cd "$(dirname "$0")/.."

CLUSTER=${CLUSTER:-ff-e2e}
NS=falcon-resource-test
PORT=18080
API="http://127.0.0.1:$PORT"
CPU_BUDGET_M=10
MEM_BUDGET_MI=48
IMAGE_BUDGET_BYTES=26214400
METRICS_SERVER=https://github.com/kubernetes-sigs/metrics-server/releases/download/v0.7.2/components.yaml

log()  { echo "==> $*"; }
fail() { echo "FAIL: $*" >&2; kubectl -n "$NS" logs deploy/falcon-resource-test --tail=50 >&2 || true; exit 1; }

# Polls a command until it succeeds or the timeout (seconds) passes.
until_ok() {
  local timeout=$1; shift
  local end=$((SECONDS + timeout))
  until "$@" >/dev/null 2>&1; do
    [ $SECONDS -lt $end ] || return 1
    sleep 5
  done
}

cleanup() {
  [ -n "${PF_PID:-}" ] && kill "$PF_PID" 2>/dev/null || true
  [ "${KEEP:-}" = 1 ] || kind delete cluster --name "$CLUSTER" >/dev/null 2>&1 || true
}
trap cleanup EXIT

log "kind cluster"
kind get clusters | grep -qx "$CLUSTER" || kind create cluster --name "$CLUSTER" --wait 2m

log "metrics-server"
kubectl apply -f "$METRICS_SERVER"
kubectl -n kube-system patch deploy metrics-server --type=json \
  -p '[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--kubelet-insecure-tls"}]'
kubectl -n kube-system rollout status deploy/metrics-server --timeout 3m

log "stand-in Falcon workloads"
kubectl apply -f test/fake-falcon.yaml
kubectl -n falcon-system rollout status ds/falcon-node-sensor --timeout 2m
kubectl -n falcon-kac rollout status deploy/falcon-kac --timeout 2m

log "image"
docker build -t falcon-resource-test:e2e .
SIZE=$(docker save falcon-resource-test:e2e | gzip | wc -c | tr -d ' ')
echo "image: $SIZE bytes compressed"
[ "$SIZE" -lt "$IMAGE_BUDGET_BYTES" ] || fail "image $SIZE bytes exceeds $IMAGE_BUDGET_BYTES"
kind load docker-image falcon-resource-test:e2e --name "$CLUSTER"

log "helm install"
INSTALLED_AT=$SECONDS
helm upgrade --install falcon-resource-test ./chart -n "$NS" --create-namespace \
  --set image.repository=falcon-resource-test --set image.tag=e2e --set image.pullPolicy=Never \
  --set clusterName=kind-test --set extraScanDurations=1m --wait --timeout 3m

kubectl -n "$NS" port-forward svc/falcon-resource-test "$PORT":80 >/dev/null 2>&1 &
PF_PID=$!
until_ok 30 curl -fsS "$API/healthz" || fail "port-forward never came up"

log "/api/setup"
SETUP=$(curl -fsS "$API/api/setup")
echo "$SETUP" | jq -e '.ok == true and .falcon_found == true' >/dev/null \
  || fail "setup not ok: $(echo "$SETUP" | jq -c '{ok, metrics, falcon_found, denied: [.permissions[] | select(.allowed|not)]}')"

log "/api/live (waiting for the first metrics tick)"
# metrics-server needs one scrape (up to 60s) before pod metrics exist.
until_ok 180 sh -c "curl -fsS $API/api/live | jq -e '.metrics_ok and .kpi.mem_bytes > 0' " \
  || fail "live never reported metrics: $(curl -fsS "$API/api/live" | jq -c '{metrics_ok, metrics_error}')"
LIVE=$(curl -fsS "$API/api/live")
echo "$LIVE" | jq -e '[.components[].component] | (index("daemonset-sensor") != null and index("kac") != null)' >/dev/null \
  || fail "stand-ins not discovered: $(echo "$LIVE" | jq -c '[.components[].component]')"
echo "$LIVE" | jq -e '.self.mem_bytes > 0' >/dev/null || fail "tool is not measuring itself"
echo "$LIVE" | jq -e '.kpi.desired >= 1 and .kpi.ready == .kpi.desired' >/dev/null \
  || fail "DaemonSet Sensor readiness: $(echo "$LIVE" | jq -c '.kpi')"

log "1m assessment"
ID=$(curl -fsS -X POST -H 'Content-Type: application/json' -d '{"duration":"1m"}' "$API/api/assessments" | jq -r .id)
[ -n "$ID" ] && [ "$ID" != null ] || fail "could not start assessment"
until_ok 150 sh -c "curl -fsS $API/api/assessments/$ID | jq -e '.assessment.status == \"complete\"'" \
  || fail "assessment $ID never completed: $(curl -fsS "$API/api/assessments/$ID" | jq -c .assessment)"
curl -fsS "$API/api/assessments/$ID" \
  | jq -e '.bundle.report.headline != "" and (.bundle.report.components | length) >= 2' >/dev/null \
  || fail "assessment $ID has no usable report"

log "export"
curl -fsS -D /tmp/ff-headers -o /tmp/ff-report.html "$API/api/assessments/$ID/export"
grep -qi '^content-disposition: attachment; filename="falcon-resource-test-kind-test-' /tmp/ff-headers \
  || fail "export file name"
for banned in '<script' 'http://' 'https://' 'src="/'; do
  grep -qF "$banned" /tmp/ff-report.html && fail "export is not self-contained: found $banned"
done
grep -q '<svg' /tmp/ff-report.html || fail "export has no chart"

log "budget gate (5 min after install)"
WAIT=$((300 - (SECONDS - INSTALLED_AT)))
[ "$WAIT" -gt 0 ] && { echo "waiting ${WAIT}s"; sleep "$WAIT"; }
FROM=$(( $(date +%s) - 240 ))   # skip the first minute of startup
SELF=$(curl -fsS "$API/api/report?from=$FROM" | jq -c '.report.self')
CPU_AVG=$(echo "$SELF" | jq '.cpu.avg')
MEM_PEAK_MI=$(echo "$SELF" | jq '(.mem.peak / 1048576 | floor)')
echo "self: cpu avg ${CPU_AVG}m (budget ${CPU_BUDGET_M}m), mem peak ${MEM_PEAK_MI}Mi (budget ${MEM_BUDGET_MI}Mi)"
kubectl -n "$NS" top pod -l app.kubernetes.io/name=falcon-resource-test || true
[ "$CPU_AVG" -lt "$CPU_BUDGET_M" ] || fail "CPU average ${CPU_AVG}m is over budget"
[ "$MEM_PEAK_MI" -lt "$MEM_BUDGET_MI" ] || fail "memory peak ${MEM_PEAK_MI}Mi is over budget"

log "PASS"
```

> **Why this matters:** The gate reads the tool's own numbers from `/api/report`, which come from the same PodMetrics the spec names, averaged over 4 minutes of steady state. A single `kubectl top` reading is one ~15s window and is too noisy to gate a build on. It's printed only as a cross-check.

> **Note:** The spec's memory target is "under 48Mi on a 50-node cluster", but kind has one node. The gate still catches regressions that don't scale with node count (a leak, a cache that holds objects, a heavy UI asset in memory). Node-count scaling is covered by the before-release check on a real cluster in Task 27.

- [ ] **Step 3: Run it**

Run: `chmod +x test/e2e.sh && ./test/e2e.sh`
Expected: about 8 minutes, ending with lines like:

```
self: cpu avg 3m (budget 10m), mem peak 27Mi (budget 48Mi)
==> PASS
```

If `/api/setup` fails with metrics not ok, metrics-server isn't ready yet or is missing `--kubelet-insecure-tls`. Run `kubectl -n kube-system logs deploy/metrics-server` to check.

If the assessment never completes, check the server's 15s refresh loop (Task 16) and that `EXTRA_SCAN_DURATIONS=1m` reached the pod: `kubectl -n falcon-resource-test get deploy falcon-resource-test -o yaml | grep -A1 EXTRA_SCAN`.

- [ ] **Step 4: Commit**

```bash
git add test/e2e.sh test/fake-falcon.yaml
git commit -m "falcon-resource-test: kind e2e and resource budget gate

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 27: CI workflow

One workflow runs on PRs, `main`, and release tags:

- **On PRs and `main`:** Go tests, UI tests, the bundle size check, chart checks, and the kind e2e with the budget gate.
- **On a `v*` tag:** everything above, then it publishes a signed multi-arch image with an SBOM, plus the OCI chart.

**Files:**
- Create: `.github/workflows/ci.yml`

- [ ] **Step 1: Write the workflow**

```yaml
name: ci

on:
  pull_request:
  push:
    branches: [main]
    tags: ["v*"]
  workflow_dispatch:

permissions:
  contents: read

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
          cache-dependency-path: go.sum

      - uses: actions/setup-node@v4
        with:
          node-version: 22
          cache: npm
          cache-dependency-path: ui/package-lock.json

      - uses: azure/setup-helm@v4

      - name: Install kubeconform
        run: |
          curl -fsSL https://github.com/yannh/kubeconform/releases/download/v0.6.7/kubeconform-linux-amd64.tar.gz \
            | sudo tar -xz -C /usr/local/bin kubeconform

      - name: Go vet and test
        run: go vet ./... && go test -race ./...

      - name: UI test, build, size budget
        run: make ui

      - name: Binary builds with the UI embedded
        run: |
          CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bin/falcon-resource-test ./cmd/falcon-resource-test
          strings bin/falcon-resource-test | grep -q 'id="root"'

      - name: Chart checks
        run: ./test/chart.sh

  e2e:
    needs: test
    runs-on: ubuntu-latest
    timeout-minutes: 20
    steps:
      - uses: actions/checkout@v4
      - uses: azure/setup-helm@v4
      - uses: helm/kind-action@v1
        with:
          cluster_name: frt-e2e
      - name: kind e2e and budget gate
        run: ./test/e2e.sh
        env:
          CLUSTER: frt-e2e
          KEEP: "1"   # kind-action owns the cluster's lifecycle

  release:
    if: startsWith(github.ref, 'refs/tags/v')
    needs: [test, e2e]
    runs-on: ubuntu-latest
    permissions:
      contents: read
      packages: write   # push image and chart to GHCR
      id-token: write   # cosign keyless signing
    env:
      IMAGE: ghcr.io/${{ github.repository_owner }}/falcon-resource-test
    steps:
      - uses: actions/checkout@v4

      - name: Version from tag
        id: v
        run: echo "version=${GITHUB_REF_NAME#v}" >> "$GITHUB_OUTPUT"

      - uses: docker/setup-qemu-action@v3
      - uses: docker/setup-buildx-action@v3
      - uses: docker/login-action@v3
        with:
          registry: ghcr.io
          username: ${{ github.actor }}
          password: ${{ secrets.GITHUB_TOKEN }}

      - name: Build and push multi-arch image
        id: build
        uses: docker/build-push-action@v6
        with:
          context: .
          platforms: linux/amd64,linux/arm64
          build-args: VERSION=${{ steps.v.outputs.version }}
          tags: |
            ${{ env.IMAGE }}:${{ steps.v.outputs.version }}
            ${{ env.IMAGE }}:latest
          push: true
          sbom: true
          provenance: mode=max

      - uses: sigstore/cosign-installer@v3

      - name: Sign the image (keyless)
        run: cosign sign --yes "${IMAGE}@${{ steps.build.outputs.digest }}"

      - uses: azure/setup-helm@v4

      - name: Package and push the chart
        run: |
          V=${{ steps.v.outputs.version }}
          helm package chart --version "$V" --app-version "$V"
          echo "${{ secrets.GITHUB_TOKEN }}" | helm registry login ghcr.io -u "${{ github.actor }}" --password-stdin
          helm push "falcon-resource-test-$V.tgz" "oci://ghcr.io/${{ github.repository_owner }}/charts"
```

> **Note:** `GITHUB_TOKEN` is the token GitHub issues per run. No secrets are stored in the repo. Keyless signing uses the run's OIDC identity (`id-token: write`), so there is no signing key to manage either.

> **Note:** The image tags use the bare version (`0.1.0`), and so does `helm package --app-version`. That's what makes the chart's default `image.tag: ""` (falls back to `.Chart.AppVersion`) resolve to the image built from the same tag.

- [ ] **Step 2: Lint the workflow**

Run (from the repo root): `command -v actionlint >/dev/null && actionlint .github/workflows/ci.yml || python3 -c "import yaml,sys; yaml.safe_load(open('.github/workflows/ci.yml')); print('yaml ok')"`
Expected: no actionlint findings, or `yaml ok`.

- [ ] **Step 3: Commit**

```bash
git add .github/workflows/ci.yml
git commit -m "falcon-resource-test: CI, signed multi-arch release, OCI chart

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

- [ ] **Step 4: First release (after the branch merges)**

Pushing a tag publishes to GHCR, so do this only with the user's go-ahead:

```bash
git tag v0.1.0 && git push origin v0.1.0
```

After the release job finishes, make the GHCR packages `falcon-resource-test` and `charts/falcon-resource-test` **public** (GitHub > Packages > Package settings > Change visibility). New GHCR packages start private, and `assess.sh` pulls anonymously.

Then verify the signature:

```bash
cosign verify ghcr.io/vianneyp72/falcon-resource-test:0.1.0 \
  --certificate-identity-regexp 'https://github.com/vianneyp72/falcon-resource-test/.github/workflows/ci.yml@.*' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

Expected: `Verification for ghcr.io/vianneyp72/falcon-resource-test:0.1.0 --` followed by the claims JSON.

**Before-release live check (spec section 15, manual).** Install the released chart on a real cluster running Falcon (DaemonSet Sensor, KAC, IAR). Then compare the Live page's per-component numbers with `kubectl top pods -n falcon-system` (and the KAC and IAR namespaces). They should agree within 10%. On a larger cluster (ideally ~50 nodes), confirm the tool's own memory stays under 48Mi on the Live page header.
