# Part 5: Optional Integrations (Tasks 17-18)

Both integrations are off by default, and the tool is complete without them. Each one adds a single outbound dependency, and the NetworkPolicy opens egress for it only when it's enabled (Task 24).

---

### Task 17: Falcon API sensor health (optional)

This task fills in the "Sensor healthy" check, which until now always reports `not_measured`. Every 5 minutes it looks up each cluster node in the Falcon Hosts API and records:
- sensor version
- online state and last seen
- reduced functionality mode (RFM)

It needs one API client with the **Hosts: Read** scope. The credentials come from a Kubernetes Secret mounted as files, never env vars, so they don't appear in `kubectl describe pod`. The tool's RBAC still has no access to Secrets: the kubelet mounts the Secret, not the tool.

**Files:**
- Create: `internal/falconapi/client.go` (OAuth2 token, Hosts lookups)
- Create: `internal/falconapi/poller.go` (5-minute poll that writes a `SnapFalconAPI` snapshot)
- Test: `internal/falconapi/client_test.go`
- Modify: `cmd/falcon-resource-test/config.go`, `cmd/falcon-resource-test/config_test.go`, `cmd/falcon-resource-test/main.go`
- Modify: `internal/web/live.go`, `internal/web/server.go`

**Endpoints used** (all covered by Hosts: Read):

| Call | Purpose |
|---|---|
| `POST /oauth2/token` | client-credentials token (cached until 60s before expiry) |
| `GET /devices/queries/devices/v1?filter=hostname:[...]` | node name to device ID, 100 names per call |
| `POST /devices/entities/devices/v2` | `hostname`, `agent_version`, `last_seen`, `reduced_functionality_mode` |
| `GET /devices/entities/online-state/v1?ids=...` | `online` / `offline` / `unknown` |

**Hostname matching:** Kubernetes node names are often FQDNs (`ip-10-0-1-5.ec2.internal`), while the sensor reports the short hostname (`ip-10-0-1-5`). The client queries by the short name (the part before the first dot), compared case-insensitively.

**Cloud selection:** `autodiscover` authenticates against US-1 and switches to the region named in the `X-Cs-Region` response header. GovCloud tenants don't redirect, so they must set `falconApi.cloud` to `us-gov-1` or `us-gov-2` explicitly.

- [ ] **Step 1: Write the failing test**

```go
package falconapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeFalcon serves just enough of the Hosts API for the client.
func fakeFalcon(t *testing.T, tokens *int) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /oauth2/token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.Form.Get("client_id") != "id" || r.Form.Get("client_secret") != "secret" {
			w.WriteHeader(401)
			return
		}
		*tokens++
		json.NewEncoder(w).Encode(map[string]any{"access_token": "tok", "expires_in": 1799})
	})
	auth := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer tok" {
				w.WriteHeader(401)
				return
			}
			h(w, r)
		}
	}
	mux.HandleFunc("GET /devices/queries/devices/v1", auth(func(w http.ResponseWriter, r *http.Request) {
		f := r.URL.Query().Get("filter")
		if !strings.Contains(f, "'ip-10-0-1-5'") || strings.Contains(f, ".ec2.internal") {
			t.Errorf("filter should use short, lowercased names: %s", f)
		}
		json.NewEncoder(w).Encode(map[string]any{"resources": []string{"d1", "d2"}})
	}))
	mux.HandleFunc("POST /devices/entities/devices/v2", auth(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"resources": []map[string]any{
			{"device_id": "d1", "hostname": "ip-10-0-1-5", "agent_version": "7.20.17308.0", "last_seen": "2026-11-19T10:00:00Z", "reduced_functionality_mode": "no"},
			{"device_id": "d2", "hostname": "IP-10-0-1-6", "agent_version": "7.20.17308.0", "last_seen": "2026-11-19T09:00:00Z", "reduced_functionality_mode": "yes"},
		}})
	}))
	mux.HandleFunc("GET /devices/entities/online-state/v1", auth(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"resources": []map[string]any{
			{"id": "d1", "state": "online"}, {"id": "d2", "state": "offline"},
		}})
	}))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestHosts(t *testing.T) {
	tokens := 0
	srv := fakeFalcon(t, &tokens)
	c := NewClient(srv.URL, "id", "secret", srv.Client())
	hosts, err := c.Hosts(context.Background(), []string{"ip-10-0-1-5.ec2.internal", "ip-10-0-1-6.ec2.internal"})
	if err != nil {
		t.Fatal(err)
	}
	if len(hosts) != 2 {
		t.Fatalf("hosts: %+v", hosts)
	}
	h := hosts[0]
	if h.Hostname != "ip-10-0-1-5.ec2.internal" || !h.Online || h.RFM || h.Version != "7.20.17308.0" ||
		!h.LastSeen.Equal(time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("host 1 (matched back to the node name): %+v", h)
	}
	if hosts[1].Online || !hosts[1].RFM {
		t.Fatalf("host 2: %+v", hosts[1])
	}
	// the token is cached across calls
	c.Hosts(context.Background(), []string{"ip-10-0-1-5"})
	if tokens != 1 {
		t.Fatalf("token requests: %d", tokens)
	}
}

func TestHostsBadCredentials(t *testing.T) {
	tokens := 0
	srv := fakeFalcon(t, &tokens)
	_, err := NewClient(srv.URL, "id", "wrong", srv.Client()).Hosts(context.Background(), []string{"n1"})
	if err == nil || !strings.Contains(err.Error(), "authentication failed") {
		t.Fatalf("want auth error, got %v", err)
	}
}

func TestBaseURL(t *testing.T) {
	for cloud, want := range map[string]string{
		"autodiscover": "https://api.crowdstrike.com",
		"us-1":         "https://api.crowdstrike.com",
		"us-2":         "https://api.us-2.crowdstrike.com",
		"eu-1":         "https://api.eu-1.crowdstrike.com",
		"us-gov-1":     "https://api.laggar.gcw.crowdstrike.com",
		"us-gov-2":     "https://api.us-gov-2.crowdstrike.mil",
	} {
		if got, err := BaseURL(cloud); err != nil || got != want {
			t.Errorf("%s: %s %v", cloud, got, err)
		}
	}
	if _, err := BaseURL("mars-1"); err == nil {
		t.Error("unknown cloud should error")
	}
}
```

- [ ] **Step 2: Run it and confirm it fails**

Run: `go test ./internal/falconapi/`
Expected: FAIL (`undefined: NewClient`)

- [ ] **Step 3: Implement `client.go`**

```go
// Package falconapi looks up cluster nodes in the Falcon Hosts API.
// Read-only: it needs a client with the Hosts: Read scope and nothing else.
package falconapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/vianneyp72/falcon-resource-test/internal/model"
)

var clouds = map[string]string{
	"autodiscover": "https://api.crowdstrike.com",
	"us-1":         "https://api.crowdstrike.com",
	"us-2":         "https://api.us-2.crowdstrike.com",
	"eu-1":         "https://api.eu-1.crowdstrike.com",
	"us-gov-1":     "https://api.laggar.gcw.crowdstrike.com",
	"us-gov-2":     "https://api.us-gov-2.crowdstrike.mil",
}

func BaseURL(cloud string) (string, error) {
	if u, ok := clouds[cloud]; ok {
		return u, nil
	}
	return "", fmt.Errorf("unknown Falcon cloud %q (use autodiscover, us-1, us-2, eu-1, us-gov-1, or us-gov-2)", cloud)
}

type Client struct {
	hc         *http.Client
	id, secret string

	mu   sync.Mutex
	base string
	tok  string
	exp  time.Time
}

func NewClient(base, id, secret string, hc *http.Client) *Client {
	if hc == nil {
		hc = &http.Client{Timeout: 20 * time.Second}
	}
	return &Client{hc: hc, base: base, id: id, secret: secret}
}

func (c *Client) token(ctx context.Context) (string, string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.tok != "" && time.Now().Before(c.exp) {
		return c.base, c.tok, nil
	}
	// Two attempts: autodiscovery may point us at the tenant's real region.
	for attempt := 0; attempt < 2; attempt++ {
		tok, ttl, region, err := c.fetchToken(ctx, c.base)
		if err != nil {
			return "", "", err
		}
		if u, ok := clouds[region]; ok && u != c.base {
			c.base = u
			continue
		}
		c.tok, c.exp = tok, time.Now().Add(ttl-time.Minute)
		return c.base, c.tok, nil
	}
	return "", "", errors.New("Falcon API: region autodiscovery did not settle; set falconApi.cloud explicitly")
}

func (c *Client) fetchToken(ctx context.Context, base string) (tok string, ttl time.Duration, region string, err error) {
	form := url.Values{"client_id": {c.id}, "client_secret": {c.secret}}
	req, _ := http.NewRequestWithContext(ctx, "POST", base+"/oauth2/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.hc.Do(req)
	if err != nil {
		return "", 0, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return "", 0, "", errors.New("Falcon API authentication failed: check the client ID and secret")
	}
	if resp.StatusCode != 201 && resp.StatusCode != 200 {
		return "", 0, "", fmt.Errorf("Falcon API token: HTTP %d", resp.StatusCode)
	}
	var body struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", 0, "", err
	}
	// US-1 names the tenant's real region in this header.
	return body.AccessToken, time.Duration(body.ExpiresIn) * time.Second, resp.Header.Get("X-Cs-Region"), nil
}

func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	base, tok, err := c.token(ctx)
	if err != nil {
		return err
	}
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequestWithContext(ctx, method, base+path, rd)
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 403 {
		return errors.New("Falcon API: the API client is missing the Hosts: Read scope")
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("Falcon API %s: HTTP %d", path, resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(out)
}

func shortName(node string) string {
	return strings.ToLower(strings.SplitN(node, ".", 2)[0])
}

// Hosts returns one SensorHost per node the Falcon cloud knows about, with
// Hostname set back to the Kubernetes node name. Nodes the cloud doesn't
// know are omitted; node coverage is the coverage check's job.
func (c *Client) Hosts(ctx context.Context, nodes []string) ([]model.SensorHost, error) {
	byShort := map[string]string{}
	var names []string
	for _, n := range nodes {
		s := shortName(n)
		if _, dup := byShort[s]; !dup {
			byShort[s] = n
			names = append(names, s)
		}
	}
	var out []model.SensorHost
	for i := 0; i < len(names); i += 100 {
		chunk := names[i:min(i+100, len(names))]
		// Node names are DNS-1123 labels, so they can't contain quotes.
		filter := "hostname:['" + strings.Join(chunk, "','") + "']"
		var ids struct{ Resources []string }
		if err := c.do(ctx, "GET", "/devices/queries/devices/v1?"+url.Values{"filter": {filter}, "limit": {"500"}}.Encode(), nil, &ids); err != nil {
			return nil, err
		}
		if len(ids.Resources) == 0 {
			continue
		}
		var devs struct {
			Resources []struct {
				DeviceID string    `json:"device_id"`
				Hostname string    `json:"hostname"`
				Version  string    `json:"agent_version"`
				LastSeen time.Time `json:"last_seen"`
				RFM      string    `json:"reduced_functionality_mode"`
			}
		}
		if err := c.do(ctx, "POST", "/devices/entities/devices/v2", map[string][]string{"ids": ids.Resources}, &devs); err != nil {
			return nil, err
		}
		var states struct {
			Resources []struct{ ID, State string }
		}
		q := url.Values{}
		for _, id := range ids.Resources {
			q.Add("ids", id)
		}
		if err := c.do(ctx, "GET", "/devices/entities/online-state/v1?"+q.Encode(), nil, &states); err != nil {
			return nil, err
		}
		online := map[string]bool{}
		for _, s := range states.Resources {
			online[s.ID] = s.State == "online"
		}
		// A reimaged node can leave a stale duplicate record; keep the
		// most recently seen one per hostname.
		best := map[string]int{}
		for _, d := range devs.Resources {
			node, ok := byShort[strings.ToLower(d.Hostname)]
			if !ok {
				continue
			}
			h := model.SensorHost{Hostname: node, Version: d.Version, Online: online[d.DeviceID], RFM: d.RFM == "yes", LastSeen: d.LastSeen}
			if j, seen := best[node]; seen {
				if h.LastSeen.After(out[j].LastSeen) {
					out[j] = h
				}
				continue
			}
			best[node] = len(out)
			out = append(out, h)
		}
	}
	return out, nil
}
```

- [ ] **Step 4: Run the tests**

Run: `gofmt -w internal/falconapi && go test ./internal/falconapi/`
Expected: `ok`

- [ ] **Step 5: Write the failing poller test** (append to `client_test.go`)

```go
func TestPoller(t *testing.T) {
	tokens := 0
	srv := fakeFalcon(t, &tokens)
	st, err := store.Open(filepath.Join(t.TempDir(), "fp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	nodes := func() []string { return []string{"ip-10-0-1-5.ec2.internal"} }

	p := NewPoller(NewClient(srv.URL, "id", "secret", srv.Client()), st, nodes)
	if p.Known() {
		t.Fatal("not known before the first poll")
	}
	p.Poll(context.Background(), time.Unix(1_700_000_000, 0))
	if !p.Known() || p.Err() != "" {
		t.Fatalf("after poll: known=%v err=%q", p.Known(), p.Err())
	}
	snap, ok, _ := st.LatestSnapshot(model.SnapFalconAPI)
	var hosts []model.SensorHost
	snap.Decode(&hosts)
	if !ok || len(hosts) != 2 {
		t.Fatalf("snapshot: %v %+v", ok, hosts)
	}

	bad := NewPoller(NewClient(srv.URL, "id", "wrong", srv.Client()), st, nodes)
	bad.Poll(context.Background(), time.Unix(1_700_000_300, 0))
	if bad.Known() || !strings.Contains(bad.Err(), "authentication failed") {
		t.Fatalf("bad creds: known=%v err=%q", bad.Known(), bad.Err())
	}
}
```

Add `"path/filepath"`, plus the `internal/model` and `internal/store` packages, to the test imports.

> **Note:** The fake returns both devices for any filter, so the snapshot holds 2 hosts even though only one node was asked about. That's fine here: the test checks the poller's bookkeeping, and `TestHosts` covers the matching.

- [ ] **Step 6: Implement `poller.go`**

```go
package falconapi

import (
	"context"
	"sync"
	"time"

	"github.com/vianneyp72/falcon-resource-test/internal/model"
	"github.com/vianneyp72/falcon-resource-test/internal/store"
)

const PollEvery = 5 * time.Minute

// Poller refreshes the SnapFalconAPI snapshot. It implements web.SensorStatus.
type Poller struct {
	c     *Client
	st    *store.Store
	nodes func() []string

	mu    sync.Mutex
	known bool
	err   string
}

func NewPoller(c *Client, st *store.Store, nodes func() []string) *Poller {
	return &Poller{c: c, st: st, nodes: nodes}
}

func (p *Poller) Poll(ctx context.Context, now time.Time) {
	cctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	hosts, err := p.c.Hosts(cctx, p.nodes())
	if err == nil {
		if hosts == nil {
			hosts = []model.SensorHost{}
		}
		err = p.st.PutSnapshot(now, model.SnapFalconAPI, hosts)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.known = err == nil
	p.err = ""
	if err != nil {
		p.err = err.Error()
	}
}

func (p *Poller) Known() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.known
}

func (p *Poller) Err() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.err
}
```

The collector needs to expose the current node names. `refreshNodes` already keeps `c.alloc` (node name to allocatable) up to date every slow tick, so derive the names from it. Add to `internal/collect/collector.go`:

```go
// NodeNames is the node list from the last slow tick. Like c.alloc, it's
// owned by the loop goroutine; the Falcon API poller runs on that loop too.
func (c *Collector) NodeNames() []string {
	names := make([]string, 0, len(c.alloc))
	for n := range c.alloc {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
```

Add `"sort"` to the imports if it isn't there already.

- [ ] **Step 7: Run the tests**

Run: `gofmt -w internal/falconapi internal/collect && go test ./internal/falconapi/ ./internal/collect/`
Expected: `ok` for both.

- [ ] **Step 8: Add the config fields** (`cmd/falcon-resource-test/config.go`)

The chart mounts the Secret at `/etc/falcon-api/` (keys `client_id`, `client_secret`) and sets `FALCON_API_DIR` only when `falconApi.enabled` is true.

Add to the `config` struct:

```go
	FalconAPIDir, FalconCloud string
```

Add to `loadConfig` before `return c, nil`:

```go
	c.FalconAPIDir = getenv("FALCON_API_DIR")
	c.FalconCloud = str("FALCON_CLOUD", "autodiscover")
	if c.FalconAPIDir != "" {
		if _, err := falconapi.BaseURL(c.FalconCloud); err != nil {
			return c, err
		}
	}
```

Import `internal/falconapi`, then add this case to the `TestLoadConfigRejects` table:

```go
		{"FALCON_API_DIR": "/etc/falcon-api", "FALCON_CLOUD": "mars-1"},
```

- [ ] **Step 9: Wire it in `main.go`**

After `col := collect.New(...)`:

```go
	var sensors *falconapi.Poller
	if cfg.FalconAPIDir != "" {
		if sensors, err = newSensorPoller(cfg, st, col); err != nil {
			return err
		}
	}
```

In `build`, replace the `false` argument to `report.Build` with `sensors != nil && sensors.Known()`.

In `web.Deps`, add `Sensors: sensorStatus(sensors),`. Then pass `sensors` to `loops` and add a ticker there:

```go
func loops(ctx context.Context, cfg config, col *collect.Collector, am *assess.Manager, st *store.Store, sensors *falconapi.Poller) {
	col.SlowTick(ctx)
	col.FastTick(ctx)
	if sensors != nil {
		sensors.Poll(ctx, time.Now())
	}
	falcon := time.NewTicker(falconapi.PollEvery)
	defer falcon.Stop()
	if sensors == nil {
		falcon.Stop()
	}
	// ... the existing fast/slow/refresh tickers ...
	for {
		select {
		// ... the existing cases ...
		case <-falcon.C:
			sensors.Poll(ctx, time.Now())
		}
	}
}
```

Add the helpers at the bottom of `main.go`:

```go
func newSensorPoller(cfg config, st *store.Store, col *collect.Collector) (*falconapi.Poller, error) {
	read := func(name string) (string, error) {
		b, err := os.ReadFile(filepath.Join(cfg.FalconAPIDir, name))
		return strings.TrimSpace(string(b)), err
	}
	id, err := read("client_id")
	if err != nil {
		return nil, fmt.Errorf("falcon API secret: %w", err)
	}
	secret, err := read("client_secret")
	if err != nil {
		return nil, fmt.Errorf("falcon API secret: %w", err)
	}
	base, _ := falconapi.BaseURL(cfg.FalconCloud) // validated in loadConfig
	return falconapi.NewPoller(falconapi.NewClient(base, id, secret, nil), st, col.NodeNames), nil
}

// sensorStatus avoids the typed-nil trap: a nil *Poller in an interface is
// not a nil interface, and web.Server checks Sensors != nil.
func sensorStatus(p *falconapi.Poller) web.SensorStatus {
	if p == nil {
		return nil
	}
	return p
}
```

Add `"fmt"`, `"strings"`, and `internal/falconapi` to the `main.go` imports.

> **Why a stopped ticker:** a stopped ticker's channel never fires, which keeps the `select` the same whether or not the integration is on.

- [ ] **Step 10: Run everything**

Run: `gofmt -l . ; go vet ./... && go test ./...`
Expected: no gofmt output; every package `ok`.

- [ ] **Step 11: Commit**

```bash
git add internal/falconapi internal/collect cmd/falcon-resource-test internal/web
git commit -m "falcon-resource-test: add optional Falcon API sensor health

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 18: Prometheus backfill (optional)

Without this, history starts when the tool is installed. If the cluster already runs Prometheus with cAdvisor metrics (kube-prometheus-stack, or most managed offerings with an in-cluster endpoint), the tool imports past Falcon usage once at startup. That way the first report can already cover a week.

How it works:
- It runs once, after the first slow tick, when discovery and node allocatable are known.
- It imports `[now - days, earliest existing sample)` at 5m resolution into the rollup tables, so it never overlaps data the tool collected itself. A restart re-imports only what's missing.
- `days` is clamped to retention. Without persistence that's 24h, so set `persistence.enabled` for the backfill to be worth it.
- Pods map to components through the discovered workloads, by namespace and longest workload-name prefix (`falcon-sensor-x7k2p` maps to the `falcon-sensor` DaemonSet).
- **Limits** (stated in the UI notes): Prometheus gives 5m averages, so backfilled peaks are smoothed. Node allocatable uses today's value. Cluster load is the sum of container usage, which slightly undercounts the system overhead metrics-server would include. Only unauthenticated endpoints are supported (typical for in-cluster Prometheus). Amazon Managed Prometheus and other SigV4 or OAuth endpoints aren't supported.

**Files:**
- Modify: `internal/store/retention.go` (add `EarliestSample`, `InsertRollups`)
- Create: `internal/prom/backfill.go`
- Test: `internal/prom/backfill_test.go`, plus a store test appended to `internal/store/series_test.go`
- Modify: `internal/collect/collector.go`, `cmd/falcon-resource-test/config.go`, `cmd/falcon-resource-test/main.go`

- [ ] **Step 1: Write the failing store test** (append to `internal/store/series_test.go`, next to the rollup tests)

```go
func TestInsertRollupsAndEarliest(t *testing.T) {
	s := open(t)
	if _, ok, err := s.EarliestSample(); ok || err != nil {
		t.Fatalf("empty store: %v %v", ok, err)
	}
	old := []model.Sample{{TS: t0.Add(-time.Hour), Node: "n1", Namespace: "falcon-system", Pod: "s1", Container: "c", Component: model.DaemonSetSensor, CPUm: 20, MemBytes: 100, CPUMaxm: 20, MemMax: 100}}
	nodes := []model.NodeSample{{TS: t0.Add(-time.Hour), Node: "n1", AllocCPUm: 2000, AllocMem: 4 << 30, UsedCPUm: 300, UsedMem: 1 << 30}}
	if err := s.InsertRollups(old, nodes); err != nil {
		t.Fatal(err)
	}
	s.InsertSamples([]model.Sample{{TS: t0, Node: "n1", Component: model.DaemonSetSensor, CPUm: 30}})
	got, ok, err := s.EarliestSample()
	if err != nil || !ok || !got.Equal(t0.Add(-time.Hour)) {
		t.Fatalf("earliest: %v %v %v", got, ok, err)
	}
	all, _ := s.Samples(t0.Add(-2*time.Hour), t0.Add(time.Minute))
	if len(all) != 2 || all[0].CPUm != 20 {
		t.Fatalf("rollup row not readable: %+v", all)
	}
}
```

`open` and `t0` are the helpers from Task 2's `store_test.go`.

- [ ] **Step 2: Run it and confirm it fails**

Run: `go test ./internal/store/ -run Rollups`
Expected: FAIL (`s.EarliestSample undefined`)

- [ ] **Step 3: Implement** (append to `internal/store/retention.go`)

```go
// EarliestSample is the oldest usage row in either table.
func (s *Store) EarliestSample() (time.Time, bool, error) {
	var ts sql.NullInt64
	err := s.db.QueryRow(`SELECT MIN(ts) FROM (SELECT MIN(ts) ts FROM samples UNION ALL SELECT MIN(ts) FROM samples_5m)`).Scan(&ts)
	if err != nil || !ts.Valid {
		return time.Time{}, false, err
	}
	return time.Unix(ts.Int64, 0), true, nil
}

// InsertRollups writes pre-aggregated 5m rows (Prometheus backfill).
func (s *Store) InsertRollups(samples []model.Sample, nodes []model.NodeSample) error {
	return s.tx(func(tx *sql.Tx) error {
		st, err := tx.Prepare(`INSERT INTO samples_5m VALUES (?,?,?,?,?,?,?,?,?,?)`)
		if err != nil {
			return err
		}
		defer st.Close()
		for _, x := range samples {
			if _, err := st.Exec(x.TS.Unix(), x.Node, x.Namespace, x.Pod, x.Container, string(x.Component), x.CPUm, x.MemBytes, x.CPUMaxm, x.MemMax); err != nil {
				return err
			}
		}
		ns, err := tx.Prepare(`INSERT INTO node_samples_5m VALUES (?,?,?,?,?,?)`)
		if err != nil {
			return err
		}
		defer ns.Close()
		for _, x := range nodes {
			if _, err := ns.Exec(x.TS.Unix(), x.Node, x.AllocCPUm, x.AllocMem, x.UsedCPUm, x.UsedMem); err != nil {
				return err
			}
		}
		return nil
	})
}
```

Add the `internal/model` import to `retention.go`.

- [ ] **Step 4: Run the store tests**

Run: `gofmt -w internal/store && go test ./internal/store/`
Expected: `ok`

- [ ] **Step 5: Write the failing backfill test**

```go
package prom

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/vianneyp72/falcon-resource-test/internal/discover"
	"github.com/vianneyp72/falcon-resource-test/internal/model"
	"github.com/vianneyp72/falcon-resource-test/internal/store"
)

var now = time.Unix(1_700_100_000, 0)

// fakeProm answers every query_range with two points 5m apart from start:
// sensor CPU 40m and memory 200Mi, node CPU 500m and memory 1Gi.
func fakeProm(t *testing.T) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/query_range" {
			w.WriteHeader(404)
			return
		}
		q := r.URL.Query().Get("query")
		start, _ := strconv.ParseFloat(r.URL.Query().Get("start"), 64)
		val, labels := "0.04", map[string]string{"namespace": "falcon-system", "pod": "falcon-sensor-x7k2p", "container": "falcon-node-sensor", "node": "n1"}
		switch {
		case strings.HasPrefix(q, "sum by (node)") && strings.Contains(q, "cpu"):
			val, labels = "0.5", map[string]string{"node": "n1"}
		case strings.HasPrefix(q, "sum by (node)"):
			val, labels = "1073741824", map[string]string{"node": "n1"}
		case strings.Contains(q, "memory"):
			val = "209715200"
		}
		if !strings.HasPrefix(q, "sum by (node)") && !strings.Contains(q, `namespace=~"falcon-system"`) {
			t.Errorf("container query not scoped to Falcon namespaces: %s", q)
		}
		json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": map[string]any{
			"resultType": "matrix",
			"result":     []map[string]any{{"metric": labels, "values": [][]any{{start, val}, {start + 300, val}}}},
		}})
	}))
}

func TestBackfill(t *testing.T) {
	srv := fakeProm(t)
	defer srv.Close()
	st, err := store.Open(filepath.Join(t.TempDir(), "fp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	// the tool has been running for an hour already
	st.InsertSamples([]model.Sample{{TS: now.Add(-time.Hour), Node: "n1", Component: model.DaemonSetSensor, CPUm: 1}})

	disc := discover.Result{
		Workloads:  []discover.Workload{{Component: model.DaemonSetSensor, Namespace: "falcon-system", Kind: "DaemonSet", Name: "falcon-sensor"}},
		Namespaces: []string{"falcon-system"},
	}
	alloc := map[string][2]int64{"n1": {2000, 4 << 30}}
	n, err := Backfill(context.Background(), NewClient(srv.URL, srv.Client()), st, disc, alloc, 7*24*time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("imported %d container rows, want 2", n)
	}
	got, _ := st.Samples(now.Add(-8*24*time.Hour), now.Add(-time.Hour))
	if len(got) != 2 || got[0].Component != model.DaemonSetSensor || got[0].CPUm != 40 || got[0].MemBytes != 200<<20 {
		t.Fatalf("rows: %+v", got)
	}
	nodes, _ := st.NodeSamples(now.Add(-8*24*time.Hour), now.Add(-time.Hour))
	if len(nodes) != 2 || nodes[0].UsedCPUm != 500 || nodes[0].AllocCPUm != 2000 {
		t.Fatalf("node rows: %+v", nodes)
	}

	// already covered: a second run imports nothing new before the earliest row
	if n, _ := Backfill(context.Background(), NewClient(srv.URL, srv.Client()), st, disc, alloc, 0, now); n != 0 {
		t.Fatalf("zero window imported %d", n)
	}
}

func TestComponentFor(t *testing.T) {
	disc := discover.Result{Workloads: []discover.Workload{
		{Component: model.KAC, Namespace: "falcon-kac", Name: "falcon-kac"},
		{Component: model.Injector, Namespace: "falcon-system", Name: "falcon-sensor-injector"},
		{Component: model.DaemonSetSensor, Namespace: "falcon-system", Name: "falcon-sensor"},
	}}
	for pod, want := range map[string]model.Component{
		"falcon-sensor-injector-6d9f8-abcde": model.Injector, // longest prefix wins
		"falcon-sensor-x7k2p":                model.DaemonSetSensor,
	} {
		if c, ok := componentFor(disc, "falcon-system", pod); !ok || c != want {
			t.Errorf("%s: %s %v", pod, c, ok)
		}
	}
	if _, ok := componentFor(disc, "falcon-system", "falcon-sensorish"); ok {
		t.Error("prefix must end at a dash")
	}
}
```

- [ ] **Step 6: Run it and confirm it fails**

Run: `go test ./internal/prom/`
Expected: FAIL (`undefined: Backfill`)

- [ ] **Step 7: Implement `backfill.go`**

```go
// Package prom imports past Falcon usage from an existing Prometheus, once,
// so the first report can cover more than the time since install.
package prom

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/vianneyp72/falcon-resource-test/internal/discover"
	"github.com/vianneyp72/falcon-resource-test/internal/model"
	"github.com/vianneyp72/falcon-resource-test/internal/store"
)

const step = 5 * time.Minute

type Client struct {
	base string
	hc   *http.Client
}

func NewClient(base string, hc *http.Client) *Client {
	if hc == nil {
		hc = &http.Client{Timeout: 60 * time.Second}
	}
	return &Client{base: strings.TrimRight(base, "/"), hc: hc}
}

type series struct {
	Metric map[string]string
	Values [][2]json.RawMessage // [unix seconds (number), value (string)]
}

func (c *Client) queryRange(ctx context.Context, q string, from, to time.Time) ([]series, error) {
	v := url.Values{
		"query": {q},
		"start": {strconv.FormatInt(from.Unix(), 10)},
		"end":   {strconv.FormatInt(to.Unix(), 10)},
		"step":  {strconv.Itoa(int(step / time.Second))},
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", c.base+"/api/v1/query_range?"+v.Encode(), nil)
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("prometheus: HTTP %d: %s", resp.StatusCode, b)
	}
	var body struct {
		Status string
		Error  string
		Data   struct{ Result []series }
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(&body); err != nil {
		return nil, err
	}
	if body.Status != "success" {
		return nil, fmt.Errorf("prometheus: %s", body.Error)
	}
	return body.Data.Result, nil
}

// point returns the timestamp (aligned to 5m) and value of one sample.
func point(p [2]json.RawMessage) (time.Time, float64, bool) {
	ts, err1 := strconv.ParseFloat(string(p[0]), 64)
	raw, err2 := strconv.Unquote(string(p[1]))
	v, err3 := strconv.ParseFloat(raw, 64)
	if err1 != nil || err2 != nil || err3 != nil {
		return time.Time{}, 0, false
	}
	return time.Unix(int64(ts)/300*300, 0), v, true
}

// componentFor maps a pod to the discovered workload whose name is the
// longest "<name>-" prefix of the pod name in the same namespace.
func componentFor(d discover.Result, ns, pod string) (model.Component, bool) {
	best, bestLen := model.Component(""), 0
	for _, w := range d.Workloads {
		if w.Namespace == ns && strings.HasPrefix(pod, w.Name+"-") && len(w.Name) > bestLen {
			best, bestLen = w.Component, len(w.Name)
		}
	}
	return best, bestLen > 0
}

// Backfill imports [now-window, earliest existing sample) and returns the
// number of container rows written.
func Backfill(ctx context.Context, c *Client, st *store.Store, d discover.Result, alloc map[string][2]int64, window time.Duration, now time.Time) (int, error) {
	from := now.Add(-window).Truncate(step)
	to := now
	if first, ok, err := st.EarliestSample(); err != nil {
		return 0, err
	} else if ok {
		to = first.Add(-time.Second)
	}
	if !from.Before(to) || len(d.Namespaces) == 0 {
		return 0, nil
	}

	quoted := make([]string, len(d.Namespaces))
	for i, ns := range d.Namespaces {
		quoted[i] = regexp.QuoteMeta(ns)
	}
	sel := fmt.Sprintf(`namespace=~"%s",container!="",container!="POD"`, strings.Join(quoted, "|"))
	all := `container!="",container!="POD"`

	type key struct {
		ts                   int64
		node, ns, pod, cname string
	}
	rows := map[key]*model.Sample{}
	get := func(ts time.Time, m map[string]string) *model.Sample {
		comp, ok := componentFor(d, m["namespace"], m["pod"])
		if !ok {
			return nil
		}
		k := key{ts.Unix(), m["node"], m["namespace"], m["pod"], m["container"]}
		if rows[k] == nil {
			rows[k] = &model.Sample{TS: ts, Node: m["node"], Namespace: m["namespace"], Pod: m["pod"], Container: m["container"], Component: comp}
		}
		return rows[k]
	}

	cpu, err := c.queryRange(ctx, fmt.Sprintf(`sum by (namespace,pod,container,node) (rate(container_cpu_usage_seconds_total{%s}[5m]))`, sel), from, to)
	if err != nil {
		return 0, err
	}
	for _, s := range cpu {
		for _, p := range s.Values {
			if ts, v, ok := point(p); ok {
				if x := get(ts, s.Metric); x != nil {
					x.CPUm = int64(v * 1000)
					x.CPUMaxm = x.CPUm
				}
			}
		}
	}
	mem, err := c.queryRange(ctx, fmt.Sprintf(`max by (namespace,pod,container,node) (container_memory_working_set_bytes{%s})`, sel), from, to)
	if err != nil {
		return 0, err
	}
	for _, s := range mem {
		for _, p := range s.Values {
			if ts, v, ok := point(p); ok {
				if x := get(ts, s.Metric); x != nil {
					x.MemBytes = int64(v)
					x.MemMax = x.MemBytes
				}
			}
		}
	}

	nodes := map[string]*model.NodeSample{}
	nodeRow := func(ts time.Time, node string) *model.NodeSample {
		a, ok := alloc[node]
		if !ok {
			return nil // node is gone; no allocatable to compare against
		}
		k := strconv.FormatInt(ts.Unix(), 10) + "/" + node
		if nodes[k] == nil {
			nodes[k] = &model.NodeSample{TS: ts, Node: node, AllocCPUm: a[0], AllocMem: a[1]}
		}
		return nodes[k]
	}
	for _, q := range []struct {
		query string
		set   func(*model.NodeSample, float64)
	}{
		{fmt.Sprintf(`sum by (node) (rate(container_cpu_usage_seconds_total{%s}[5m]))`, all), func(n *model.NodeSample, v float64) { n.UsedCPUm = int64(v * 1000) }},
		{fmt.Sprintf(`sum by (node) (container_memory_working_set_bytes{%s})`, all), func(n *model.NodeSample, v float64) { n.UsedMem = int64(v) }},
	} {
		res, err := c.queryRange(ctx, q.query, from, to)
		if err != nil {
			return 0, err
		}
		for _, s := range res {
			for _, p := range s.Values {
				if ts, v, ok := point(p); ok {
					if n := nodeRow(ts, s.Metric["node"]); n != nil {
						q.set(n, v)
					}
				}
			}
		}
	}

	samples := make([]model.Sample, 0, len(rows))
	for _, x := range rows {
		samples = append(samples, *x)
	}
	ns := make([]model.NodeSample, 0, len(nodes))
	for _, n := range nodes {
		ns = append(ns, *n)
	}
	if err := st.InsertRollups(samples, ns); err != nil {
		return 0, err
	}
	return len(samples), nil
}
```

> **Note:** The queries rely on cAdvisor series carrying a `node` label. kube-prometheus-stack's kubelet ServiceMonitor adds it. If your Prometheus scrapes cAdvisor some other way, add the label with a relabel rule, or the container rows import without node attribution and the node rows are skipped.

- [ ] **Step 8: Run the tests**

Run: `gofmt -w internal/prom && go test ./internal/prom/`
Expected: `ok`

- [ ] **Step 9: Expose node allocatable** (append to `internal/collect/collector.go`)

```go
// NodeAllocatable is node -> [cpu_m, mem] from the last slow tick. Owned by
// the loop goroutine, like NodeNames.
func (c *Collector) NodeAllocatable() map[string][2]int64 {
	out := make(map[string][2]int64, len(c.alloc))
	for k, v := range c.alloc {
		out[k] = v
	}
	return out
}
```

- [ ] **Step 10: Config** (`cmd/falcon-resource-test/config.go`)

Add to the `config` struct:

```go
	PromURL      string
	BackfillDays int
```

Add to `loadConfig` before `return c, nil`:

```go
	c.PromURL = getenv("PROMETHEUS_URL")
	if c.BackfillDays, err = strconv.Atoi(str("PROMETHEUS_BACKFILL_DAYS", "7")); err != nil || c.BackfillDays < 0 {
		return c, fmt.Errorf("PROMETHEUS_BACKFILL_DAYS must be a whole number of days")
	}
	if c.PromURL != "" {
		if u, err := url.Parse(c.PromURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return c, fmt.Errorf("PROMETHEUS_URL must be an http(s) URL, got %q", c.PromURL)
		}
	}
```

Add `"net/url"` to the imports, then add this case to `TestLoadConfigRejects`:

```go
		{"PROMETHEUS_URL": "prometheus:9090"}, // missing scheme
```

- [ ] **Step 11: Run the backfill once from the loop** (`cmd/falcon-resource-test/main.go`)

In `loops`, right after the first `col.SlowTick(ctx)` / `col.FastTick(ctx)`:

```go
	if cfg.PromURL != "" {
		backfill(ctx, cfg, col, st)
	}
```

Add the helper:

```go
// backfill runs once, inline on the loop: it's a handful of range queries,
// and running it here keeps the store single-writer.
func backfill(ctx context.Context, cfg config, col *collect.Collector, st *store.Store) {
	window := min(time.Duration(cfg.BackfillDays)*24*time.Hour, cfg.Retention)
	bctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	n, err := prom.Backfill(bctx, prom.NewClient(cfg.PromURL, nil), st, col.Status().Discovery, col.NodeAllocatable(), window, time.Now())
	if err != nil {
		slog.Warn("prometheus backfill failed; continuing without history", "err", err)
		return
	}
	slog.Info("prometheus backfill", "rows", n, "window", window)
}
```

Add `internal/prom` to the imports.

- [ ] **Step 12: Run everything**

Run: `gofmt -l . ; go vet ./... && go test ./...`
Expected: no gofmt output; every package `ok`.

- [ ] **Step 13: Commit**

```bash
git add internal/store internal/prom internal/collect cmd/falcon-resource-test
git commit -m "falcon-resource-test: add optional Prometheus backfill

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```
