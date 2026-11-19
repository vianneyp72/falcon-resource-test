# 08 — Docs and site integration

Tasks 28–29. They wire the tool into the lab site and the repo docs. Every page here follows the repo `CLAUDE.md`:

- Dual-mode (`data-mode="guide"` and `data-mode="lab"`).
- Terse prerequisites with hover tooltips.
- Callout ledes from the taxonomy.
- No emojis.
- No time estimates.
- Written for an SE who is new to cloud.

---

### Task 28: Site guide + manifest placement

**Files:**
- Create: `se-built/falcon-resource-test.md`
- Modify: `webapp/src/content/manifest.js` (imports ~49-52, `labs` map ~109-112, `LABS` ~239-242, tree ~511-515)

- [ ] **Step 1: Write `se-built/falcon-resource-test.md` (header, Core Concepts, Quick Deploy)**

````markdown
# Falcon Resource Test — Measure Falcon's CPU and Memory in Your Cluster

A read-only, in-cluster tool that shows live CPU, memory, and health for every Falcon component in a Kubernetes cluster. It produces an exportable report a platform team can sign off on.

> **Note:** SE-built, not an official CrowdStrike product. Source, chart, and image build live in this repo under ``.

> **Prerequisites:**
>
> - Kubernetes 1.25 or later <span data-tip="EKS, GKE, AKS, OpenShift, or self-managed. kubectl version should show a server version of 1.25 or newer.">cluster</span>
> - Falcon <span data-tip="DaemonSet Sensor, KAC, IAR, or the sidecar injector, installed with Helm or the Falcon Operator. Without Falcon the tool still runs and shows an empty state.">already deployed</span>
> - `kubectl` and Helm 3.8 or later <span data-tip="Helm 3.8 is the first release that installs charts from OCI registries. Run helm version to check.">installed</span>
> - metrics-server <span data-tip="Provides live pod CPU and memory. EKS and AKS often ship without it; the tool's Setup page detects this and prints the install command.">running in the cluster</span>
> - Permission to <span data-tip="The chart creates a ClusterRole and ClusterRoleBinding, which needs cluster-admin or an equivalent role. The tool itself only ever reads.">create RBAC objects</span>

## Reference Docs

| Source | Link |
| ------ | ---- |
| Tool source and chart | `` in this repo |
| Kubernetes metrics-server | https://github.com/kubernetes-sigs/metrics-server |

---

## Core Concepts

Falcon Resource Test runs as a single small pod (10m CPU and 32Mi memory requested). It finds the Falcon workloads in the cluster and samples their usage every 15 seconds, keeping the history on local disk.

- **Live** — current usage per Falcon component and per node, a stacked history chart, and health checks such as restarts, OOM kills, and how much of their requests the components actually use.
- **Assessment** — a timed scan (5 minutes to 24 hours) or an instant report over history the tool already has. It produces averages, p95, and peaks, and you export it as one self-contained HTML file.
- **Setup** — checks every permission the tool uses and whether metrics-server answers.

> **Why this matters:** Kubernetes bills a pod for the CPU it *requests*, not only what it uses. A platform team cares about both: requests decide how many workloads fit on a node, and usage shows the real cost. The report shows the two side by side for every Falcon component.

The tool is **read-only**. It lists pods, nodes, events, DaemonSets, Deployments, webhooks, and metrics. It cannot read Secrets, exec into pods, or change anything.

---

## Deployment Steps

<div data-mode="guide">

### 1. Install and open the UI

<div data-fill-fields="CLUSTER_NAME:Cluster display name"></div>

From a checkout of this repo, run the one-command installer. It installs the chart into the `falcon-resource-test` namespace, waits for the pod, and opens a port-forward to the UI:

```bash
./scripts/assess.sh --set clusterName=<CLUSTER_NAME>
```

> **Verify:** The script prints `falcon-resource-test is running` and your browser opens `http://localhost:8080` on the **Live** page. Keep the terminal open; closing it stops the port-forward, not the tool.

<details><summary>Plain Helm equivalent</summary>

```bash
helm upgrade --install falcon-resource-test oci://ghcr.io/vianneyp72/charts/falcon-resource-test \
  --namespace falcon-resource-test --create-namespace --wait \
  --set clusterName=<CLUSTER_NAME>
```

```bash
kubectl -n falcon-resource-test port-forward svc/falcon-resource-test 8080:80
```

</details>

> **Heads-up:** If the Live page shows a **metrics-server** banner, the cluster has no metrics API. Open **Setup** for the exact install command, run it, and the numbers appear within a minute.

### 2. Run an assessment

On the **Live** page, click **Run assessment**. Under **Watch from now**, pick **15m** (or **24h** for a full business day), then click **Start 15m scan**. The report fills in as data arrives.

> **Tip:** If the tool has already been running for a while, pick a **Last 1h** option under **Or report on data already collected** and click **Build report**. You get a report immediately, with no wait.

### 3. Export the report

When the scan ends and the progress bar disappears, click **Export HTML**. The file has no external links or scripts, so you can email it or attach it to a change ticket.

> **Look for:** The headline at the top (for example "Over 15 minutes on 3 nodes, Falcon used 0.4% of cluster CPU and 1.1% of memory, with 0 restarts and 3/3 node coverage.") and the checks list. Anything marked **Fail** or **Review** is what to discuss with the customer.

### 4. Clean up

```bash
helm -n falcon-resource-test uninstall falcon-resource-test
```

```bash
kubectl delete namespace falcon-resource-test
```

> **Verify:** `kubectl get clusterrole falcon-resource-test` returns `NotFound`. Nothing else is left behind.

</div>
````

- [ ] **Step 2: Append the Full Lab, sections 1–3 (install, Live, assessment)**

Append to `se-built/falcon-resource-test.md`:

````markdown

<div data-mode="lab">

## 1. Install Falcon Resource Test

> **What & Why:** You install one Deployment, one Service, and read-only RBAC into their own namespace. A separate namespace keeps the tool easy to find and to remove completely.

### Step 1: Confirm cluster access and metrics

- [ ] Confirm `kubectl` points at the cluster you mean to measure:

```bash
kubectl config current-context
```

> **Heads-up:** If this prints the wrong cluster, switch first. On EKS run `aws eks update-kubeconfig --name <CLUSTER_NAME> --region <REGION>`, on GKE `gcloud container clusters get-credentials <CLUSTER_NAME> --region <REGION>`, and on AKS `az aks get-credentials -g <RESOURCE_GROUP> -n <CLUSTER_NAME>`.

- [ ] Check that the metrics API answers:

```bash
kubectl top nodes
```

> **Verify:** One row per node with `CPU(cores)` and `MEMORY(bytes)`. If you see `Metrics API not available`, install metrics-server:
>
> `kubectl apply -f https://github.com/kubernetes-sigs/metrics-server/releases/latest/download/components.yaml`

> **Why this matters:** metrics-server is the cluster add-on that reports live pod and node usage, and it's the same data `kubectl top` shows. GKE ships it by default. EKS and many AKS clusters do not.

### Step 2: Install the chart

<div data-fill-fields="CLUSTER_NAME:Cluster display name"></div>

- [ ] Install into the `falcon-resource-test` namespace:

```bash
helm upgrade --install falcon-resource-test oci://ghcr.io/vianneyp72/charts/falcon-resource-test \
  --namespace falcon-resource-test --create-namespace --wait \
  --set clusterName=<CLUSTER_NAME>
```

> **Verify:** Helm prints `STATUS: deployed` and the NOTES text with the port-forward command.

- [ ] Check the pod:

```bash
kubectl -n falcon-resource-test get pods
```

> **Verify:** One pod, `READY` `1/1`, `STATUS` `Running`.

> **Tip:** The pod's security settings meet the Kubernetes **restricted** Pod Security Standard: non-root, read-only root filesystem, no capabilities. Clusters that enforce restricted admission accept it without exceptions.

### Step 3: Open the UI

- [ ] Forward a local port to the tool's Service:

```bash
kubectl -n falcon-resource-test port-forward svc/falcon-resource-test 8080:80
```

- [ ] Open `http://localhost:8080` in your browser.

> **Why this matters:** A port-forward tunnels one local port to the Service through the Kubernetes API, using your own kubectl credentials. Nothing is exposed outside the cluster. That's why the chart's Service is `ClusterIP` and the Ingress is off by default.

## 2. Read the Live Page

> **What & Why:** Live answers "what is Falcon costing this cluster right now?" Walk through it with the customer before you start a scan.

- [ ] Open **Setup** first and confirm the three checks pass.

> **Verify:** **Kubernetes Metrics API**, **Read-only permissions**, and **Falcon components discovered** all pass, and every row in the **Permissions** table says `granted`. A `denied` row's **Used for** column says which feature is affected.

- [ ] Back on **Live**, read the KPI row.

> **Look for:**
>
> - **Falcon CPU** and **Falcon memory**: total usage and its share of cluster allocatable.
> - **DaemonSet Sensor pods**: ready out of desired. It should match the node count.
> - **Restarts (24h)**: should be 0.

- [ ] Read the **Components** table. Click a row to filter the chart to that component, or click a node in **Nodes** to see one node.

> **Why this matters:** **Request / pod** is what the scheduler sets aside for each pod, whether or not it uses it. **CPU** and **Memory** are what Falcon actually consumes. A large gap between the two is capacity the customer can reclaim by lowering requests. That's often the first question a platform team asks.

- [ ] Read the **Checks** panel. Items are sorted with **Fail** first.

> **Note:** **Not measured** means the data isn't available from the cluster (for example sensor version without the optional Falcon API). It does not mean something is wrong.

## 3. Run an Assessment and Read the Report

> **What & Why:** Live is a snapshot. An assessment gives averages, p95, and peaks over a window, frozen into a report the customer can keep.

- [ ] Click **Run assessment**. Under **Watch from now**, pick **15m** for a call, or **24h** for the strongest evidence. Click **Start 15m scan**.

> **Tip:** Ask the customer to run their normal or peak workload during the window. Falcon's CPU tracks process and network activity, so an idle cluster understates it.

- [ ] Leave the page open or come back later. Closing the tab doesn't stop the scan.

> **Heads-up:** Without persistence, history lives on an emptyDir and is lost if the pod restarts. A scan that was running when that happened shows **interrupted**. For 24h scans, turn on persistence (Section 5).

- [ ] When the scan ends, read the report top to bottom:

> **Look for:**
>
> - **Headline**: one sentence with Falcon's share of cluster CPU and memory, restarts, and coverage.
> - **Components**: avg, p95, and peak per component, next to requests and limits.
> - **Checks**: each one is **Pass**, **Review**, or **Fail**, with the reason.
> - **Methodology**: the sampling interval and window, so a reviewer can trust the numbers.

- [ ] Click **Export HTML** and share the file.

> **Note:** The export is one HTML file with inline charts, and no scripts or external links. It opens offline and is safe to attach to a change ticket.

````

- [ ] **Step 3: Append Full Lab sections 4–7, Challenges, Quick Reference**

Append to `se-built/falcon-resource-test.md`:

````markdown

## 4. Install with GitOps (Argo CD or Flux)

> **What & Why:** Platform teams rarely run `helm install` by hand in production. They commit an Argo CD or Flux object, and their controller installs the chart. Hand them the snippet that matches their tool.

- [ ] **Argo CD:** commit this `Application`:

```yaml
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: falcon-resource-test
  namespace: argocd
spec:
  project: default
  source:
    repoURL: ghcr.io/vianneyp72/charts
    chart: falcon-resource-test
    targetRevision: 0.1.0
    helm:
      valuesObject:
        clusterName: <CLUSTER_NAME>
  destination:
    server: https://kubernetes.default.svc
    namespace: falcon-resource-test
  syncPolicy:
    automated: { prune: true }
    syncOptions: [CreateNamespace=true]
```

> **Heads-up:** Argo CD reads OCI charts only when the registry is added as a Helm repository with OCI enabled. If the sync errors with `repository not found`, add it once: `argocd repo add ghcr.io/vianneyp72/charts --type helm --enable-oci --name ghcr-charts`.

- [ ] **Flux:** commit an `OCIRepository` and a `HelmRelease`:

```yaml
apiVersion: source.toolkit.fluxcd.io/v1beta2
kind: OCIRepository
metadata:
  name: falcon-resource-test
  namespace: flux-system
spec:
  interval: 1h
  url: oci://ghcr.io/vianneyp72/charts/falcon-resource-test
  ref:
    tag: 0.1.0
---
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: falcon-resource-test
  namespace: flux-system
spec:
  interval: 10m
  chartRef:
    kind: OCIRepository
    name: falcon-resource-test
  targetNamespace: falcon-resource-test
  install:
    createNamespace: true
  values:
    clusterName: <CLUSTER_NAME>
```

> **Verify:** In Argo CD the app shows **Synced** and **Healthy**. In Flux, `flux get helmreleases -n flux-system` shows `falcon-resource-test` with `READY` `True`.

> **Tip:** Pin `targetRevision` / `tag` to a version, not `latest`. The image tag follows the chart version, so the pin fixes both.

## 5. Optional: Persistence, Private Registry, and Integrations

> **What & Why:** The defaults suit a one-hour call. These options suit a week-long proof of value or a locked-down cluster. Each is a Helm value.

### Keep history across restarts

- [ ] Turn on a PersistentVolumeClaim:

```bash
helm upgrade falcon-resource-test oci://ghcr.io/vianneyp72/charts/falcon-resource-test \
  -n falcon-resource-test --reuse-values --set persistence.enabled=true
```

> **Why this matters:** A PersistentVolumeClaim asks the cluster for a disk that outlives the pod (an EBS volume on EKS, a Persistent Disk on GKE, a Managed Disk on AKS). Without one, history lives on an emptyDir, which is deleted with the pod. Retention becomes 30 days (`persistence.retentionDays`).

> **Heads-up:** Switching to persistence replaces the emptyDir, so history collected so far is lost once. Export anything you need first.

### Air-gapped or private registry

- [ ] Build the image from this repo and push it to the customer's registry:

```bash
cd falcon-resource-test
docker buildx build --platform linux/amd64,linux/arm64 -t <YOUR_REGISTRY>/falcon-resource-test:0.1.0 --push .
```

- [ ] Install from the local chart, pointing at that image:

```bash
helm upgrade --install falcon-resource-test ./chart -n falcon-resource-test --create-namespace \
  --set image.repository=<YOUR_REGISTRY>/falcon-resource-test --set image.tag=0.1.0
```

> **Note:** If the registry needs credentials, create a pull secret in `falcon-resource-test` and add `--set imagePullSecrets[0].name=<SECRET_NAME>`.

### Sensor version and online state (Falcon API)

- [ ] Create an API client with **Hosts** (Read) in **Support and resources** > **Client Management** > **API Clients**, and enter it here:

<div data-fill-fields="FALCON_CLIENT_ID:API client ID,FALCON_CLIENT_SECRET:API client secret"></div>

- [ ] Export the secret. It is the only value kept as a shell variable, and the page never saves it:

```bash
export FALCON_CLIENT_SECRET="<FALCON_CLIENT_SECRET>"
```

- [ ] Store both as a Kubernetes Secret in the tool's namespace:

```bash
kubectl -n falcon-resource-test create secret generic falcon-api \
  --from-literal=client_id=<FALCON_CLIENT_ID> --from-literal=client_secret=$FALCON_CLIENT_SECRET
```

- [ ] Point the chart at the Secret:

```bash
helm upgrade falcon-resource-test oci://ghcr.io/vianneyp72/charts/falcon-resource-test \
  -n falcon-resource-test --reuse-values \
  --set falconApi.enabled=true --set falconApi.existingSecret=falcon-api
```

> **Heads-up:** On GovCloud, also set `--set falconApi.cloud=us-gov-1` (or `us-gov-2`). Autodiscovery doesn't cross into GovCloud.

> **Note:** The kubelet mounts the Secret into the pod as files. The tool's own RBAC still has no access to Secrets.

### Import past usage from Prometheus

- [ ] If the cluster already runs Prometheus, backfill the last 7 days so the first report has real history:

```bash
helm upgrade falcon-resource-test oci://ghcr.io/vianneyp72/charts/falcon-resource-test \
  -n falcon-resource-test --reuse-values \
  --set prometheus.url=http://prometheus-server.monitoring.svc:80
```

> **Verify:** The Live chart's **24h** range fills in within a minute of the pod restarting.

## 6. Clean Up

> **What & Why:** Remove everything the tool created. Falcon itself is not touched.

- [ ] Uninstall the release:

```bash
helm -n falcon-resource-test uninstall falcon-resource-test
```

- [ ] Delete the namespace, which also removes the PVC and any Secret you created:

```bash
kubectl delete namespace falcon-resource-test
```

> **Verify:** `kubectl get clusterrole,clusterrolebinding falcon-resource-test` returns `NotFound` for both.

> **Destructive:** Deleting the namespace deletes the stored history and saved assessments. Export the reports you need first.

## Challenges

1. **Load test.** Start a **1h** scan, then run a CPU-heavy workload in another namespace (for example `kubectl create deploy stress --image=polinux/stress -- stress --cpu 2`). Compare Falcon's p95 and peak with the idle baseline. Delete the deployment afterwards.
2. **Lock down egress.** Find the API server's IP (`kubectl get endpoints kubernetes -n default`) and set `networkPolicy.apiServerCIDRs` to it. Confirm the Live page still updates.
3. **Prove it's read-only.** Run `kubectl auth can-i --list --as=system:serviceaccount:falcon-resource-test:falcon-resource-test` and confirm every verb is `list`, apart from `create` on `selfsubjectaccessreviews`.

## Quick Reference

| Task | Command or setting |
| ---- | ------------------ |
| Install | `helm upgrade --install falcon-resource-test oci://ghcr.io/vianneyp72/charts/falcon-resource-test -n falcon-resource-test --create-namespace` |
| Open the UI | `kubectl -n falcon-resource-test port-forward svc/falcon-resource-test 8080:80` |
| Keep history | `--set persistence.enabled=true` |
| Limit discovery | `--set discovery.namespaces={falcon-system,falcon-kac}` |
| Falcon API | `--set falconApi.enabled=true --set falconApi.existingSecret=falcon-api` |
| Private image | `--set image.repository=<YOUR_REGISTRY>/falcon-resource-test` |
| Logs | `kubectl -n falcon-resource-test logs deploy/falcon-resource-test` |
| Uninstall | `helm -n falcon-resource-test uninstall falcon-resource-test && kubectl delete namespace falcon-resource-test` |

</div>
````

- [ ] **Step 4: Register the page in `webapp/src/content/manifest.js`**

Add the import after the `seAzurePreReqs` import (line ~52):

```js
import seFalconResourceTest from '@content/se-built/falcon-resource-test.md?raw'
```

Add to the `labs` map after `'se-built/azure-fcs-prereqs-checker.md': seAzurePreReqs,` (line ~112):

```js
  'se-built/falcon-resource-test.md': seFalconResourceTest,
```

Add to `LABS` after the `se-built/azure-fcs-prereqs-checker` entry (line ~242):

```js
  { id: 'se-built/falcon-resource-test',             label: 'Falcon Resource Test',             area: 'SE-Built', md: 'se-built/falcon-resource-test.md',             reference: true },
```

Add the placement in the Tooling tree, inside `g('SE-Built Tools', 'se-built', [...])`, after the `azure-fcs-prereqs-checker` line (line ~515):

```js
        l('se-built/falcon-resource-test', 'falcon-resource-test'),
```

> **Note:** `reference: true` matches the other SE-Built entries. It keeps the tool off the deployment-lab roadmap (`/roadmap`), which tracks Falcon deployment methods, not tools.

- [ ] **Step 5: Build the site and check the page**

Run: `cd webapp && npm run build`
Expected: the build succeeds. The manifest throws at build time if a `LABS` entry has no placement or an `md` path has no import.

Run: `cd webapp && npm run dev`, then open the dev server URL > **Tooling** > **SE-Built Tools** > **Falcon Resource Test**.

Expected:
- **Quick Deploy** and **Full Lab** toggle.
- Prerequisite tooltips appear on hover.
- `<CLUSTER_NAME>` fills from the panel.
- Callouts are colored (blue What & Why, teal Tip / Why this matters, yellow Heads-up, red Destructive, green Verify / Look for).
- Searching "resource test" finds the page.

- [ ] **Step 6: Commit**

```bash
git add se-built/falcon-resource-test.md webapp/src/content/manifest.js
git commit -m "falcon-resource-test: site guide under Tooling > SE-Built Tools

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

(Run from the repo root; this task's paths are repo-relative, unlike the tool tasks.)

---

### Task 29: Tool README + repo README/CLAUDE.md

**Files:**
- Create: `README.md`
- Modify: `README.md` (decision matrix, repository structure)
- Modify: `CLAUDE.md` (intro line, file conventions, a "Tools" section)

- [ ] **Step 1: Write `README.md`**

The site page (Task 28) is for SEs running the tool. This README is for engineers working on it.

````markdown
# falcon-resource-test

Read-only, in-cluster tool that measures what CrowdStrike Falcon costs a Kubernetes cluster: live CPU, memory, and health per Falcon component, plus exportable assessment reports.

**Using it:** see the site guide, Tooling > SE-Built Tools > Falcon Resource Test (`se-built/falcon-resource-test.md`).

## Layout

| Path | What it is |
| ---- | ---------- |
| `cmd/falcon-resource-test/` | Entry point and env config |
| `internal/collect/` | Polls metrics.k8s.io and the K8s API (no informers) |
| `internal/store/` | SQLite storage, rollups, retention, assessments |
| `internal/discover/` | Finds Falcon workloads by image name or `app.kubernetes.io/name` |
| `internal/analyze/` | Stats and health checks |
| `internal/report/` | Self-contained HTML export |
| `internal/web/` | JSON API and embedded UI |
| `internal/setup/` | Preflight checks; `Required` is the RBAC source of truth |
| `internal/falconapi/`, `internal/prom/` | Optional integrations |
| `ui/` | React + Vite + uPlot; builds into `internal/web/dist` |
| `chart/` | Helm chart |
| `scripts/assess.sh` | One-command install and port-forward |
| `test/` | Chart checks and the kind e2e / budget gate |

## Develop

Needs Go 1.24+, Node 22+, Docker, Helm, and kind (for e2e).

```bash
make test        # Go unit tests
make ui          # UI tests, build, bundle size budget
make build       # bin/falcon-resource-test with the UI embedded
make chart-test  # helm lint + render checks
make e2e         # kind cluster, install, assessment, export, budget gate
```

UI dev server, proxying the API to a port-forwarded instance:

```bash
kubectl -n falcon-resource-test port-forward svc/falcon-resource-test 8080:80
cd ui && npm run dev
```

After `npm install` of a new package, run `npm run fix-lockfile` before committing (rewrites internal registry URLs to the public npm registry).

## Budgets (CI-enforced)

| | Budget | Where |
| - | ------ | ----- |
| CPU | under 10m average | `test/e2e.sh` |
| Memory | under 48Mi | `test/e2e.sh` |
| Image | under 25 MiB compressed | `test/e2e.sh` |
| UI JS | under 120 KB gzipped | `scripts/check-ui-size.sh` |

## Rules

- RBAC is read-only. Adding a permission means changing `internal/setup.Required` and `chart/templates/rbac.yaml` together; `TestChartRBACMatchesRequired` fails if they drift. Never add write verbs or Secrets.
- No informers or object caches. Poll and drop.
- Runtime UI dependencies are react, react-dom, and uplot only.

## Release

Push a tag `vX.Y.Z`. CI publishes `ghcr.io/vianneyp72/falcon-resource-test:X.Y.Z` (multi-arch, cosign-signed, SBOM) and the chart to `oci://ghcr.io/vianneyp72/charts/falcon-resource-test`.
````

- [ ] **Step 2: Update the repo `README.md`**

Add a row at the end of the Decision Matrix table, after the **Cloud account registration** row:

```markdown
| **Measure Falcon's footprint** (any K8s) | In-cluster tool + Helm chart | [se-built/falcon-resource-test.md](se-built/falcon-resource-test.md) |
```

In the Repository Structure tree, add `tools/` between `se-built/` and `webapp/`:

```
├── se-built/                         # SE-built tools and references
├── tools/                            # Tools with source code
│   └── falcon-resource-test/             # Go + React tool, Helm chart (see its README)
└── webapp/                           # React site that renders the labs
```

- [ ] **Step 3: Update the repo `CLAUDE.md`**

Replace the intro sentence's ending, "No application code, no tests, no builds — just markdown, shell scripts, and Terraform files.", with:

```markdown
Almost everything is markdown, shell scripts, and Terraform. The one exception is `tools/`, which holds tools with real source code, tests, and builds (today: ``).
```

Add to **File conventions**, after the `_common/lab-authoring-features.md` bullet:

```markdown
- `tools/<name>/` — A tool with source code. It has its own `README.md`, `Makefile`, and tests, and its user-facing guide lives in `se-built/<name>.md`
```

Add a new section before **Webapp dependency management**:

```markdown
## Tools (`tools/`)

- Each tool is self-contained: build, test, and release from its own folder (`make test`, `make e2e`). CI lives in `.github/workflows/<name>.yml`, filtered to the tool's path.
- `` is read-only by design. Never add write verbs or Secrets access to its RBAC; `internal/setup.Required` and `chart/templates/rbac.yaml` must change together.
- Its UI has the same lockfile rule as `webapp/`: run `npm run fix-lockfile` in `ui` before committing.
- Tool folders get a `README.md` (for developers). The "no README in lab folders" rule below applies to lab folders only.
```

- [ ] **Step 4: Check the links**

Run: `grep -n "falcon-resource-test" README.md CLAUDE.md && ls se-built/falcon-resource-test.md README.md`
Expected: the new lines are printed and both files exist.

- [ ] **Step 5: Commit**

```bash
git add README.md README.md CLAUDE.md
git commit -m "falcon-resource-test: README, and document tools/ in repo docs

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

(Run from the repo root.)