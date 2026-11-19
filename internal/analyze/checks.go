package analyze

import (
	"fmt"
	"sort"
	"time"

	"github.com/vianneyp72/falcon-resource-test/internal/model"
)

type Status string

const (
	Pass        Status = "pass"
	Fail        Status = "fail"
	Review      Status = "review"
	Info        Status = "info"
	NotMeasured Status = "not_measured"
)

type Check struct {
	ID      string   `json:"id"`
	Title   string   `json:"title"`
	Status  Status   `json:"status"`
	Summary string   `json:"summary"`
	Details []string `json:"details,omitempty"`
}

// TimedDaemonSets is one SnapDaemonSets snapshot (DaemonSet Sensor only).
type TimedDaemonSets struct {
	TS    time.Time
	State []model.DaemonSetState
}

func plural(n int, word string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", word)
	}
	return fmt.Sprintf("%d %ss", n, word)
}

func checkStable(events []model.Event, falconSeen bool) Check {
	c := Check{ID: "stable", Title: "Stable"}
	if !falconSeen {
		c.Status, c.Summary = NotMeasured, "No Falcon pods found in this window."
		return c
	}
	counts := map[model.EventKind]int{}
	for _, e := range events {
		if !e.Falcon {
			continue
		}
		switch e.Kind {
		case model.EventRestart, model.EventOOMKill, model.EventCrashLoop:
			counts[e.Kind]++
			c.Details = append(c.Details, fmt.Sprintf("%s %s/%s at %s: %s", e.Kind, e.Namespace, e.Object, e.TS.UTC().Format(time.RFC3339), e.Reason))
		}
	}
	r, o, l := counts[model.EventRestart], counts[model.EventOOMKill], counts[model.EventCrashLoop]
	c.Summary = fmt.Sprintf("%s, %s, %s.", plural(r, "restart"), plural(o, "OOMKill"), plural(l, "CrashLoop"))
	if r+o+l == 0 {
		c.Status = Pass
	} else {
		c.Status = Fail
	}
	return c
}

// checkLane compares each container's peak memory with its limit. Limits
// are keyed by (component, container name) because pods rotate but the
// workload template doesn't.
func checkLane(specs []model.ContainerSpec, samples []model.Sample) Check {
	c := Check{ID: "lane", Title: "Stays in its lane"}
	if len(samples) == 0 {
		c.Status, c.Summary = NotMeasured, "No usage samples in this window (metrics-server unavailable or Falcon not running)."
		return c
	}
	type key struct {
		comp model.Component
		name string
	}
	limMem, limCPU := map[key]int64{}, map[key]int64{}
	for _, s := range specs {
		if s.LimMem > 0 {
			limMem[key{s.Component, s.Container}] = s.LimMem
		}
		if s.LimCPUm > 0 {
			limCPU[key{s.Component, s.Container}] = s.LimCPUm
		}
	}
	if len(limMem) == 0 {
		c.Status, c.Summary = Info, "No memory limits are set on Falcon containers, so there is no ceiling to compare against."
		return c
	}
	peakMem, peakCPU := map[key]int64{}, map[key]int64{}
	for _, s := range samples {
		k := key{s.Component, s.Container}
		if s.MemMax > peakMem[k] {
			peakMem[k] = s.MemMax
		}
		if s.CPUMaxm > peakCPU[k] {
			peakCPU[k] = s.CPUMaxm
		}
	}
	keys := make([]key, 0, len(limMem))
	for k := range limMem {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		return keys[i].comp+model.Component(keys[i].name) < keys[j].comp+model.Component(keys[j].name)
	})
	hit := 0
	for _, k := range keys {
		lim, peak := limMem[k], peakMem[k]
		c.Details = append(c.Details, fmt.Sprintf("%s / %s: peak %s of %s limit (%.0f%%)", k.comp.Label(), k.name, MiB(peak), MiB(lim), Pct(peak, lim)))
		if peak >= lim {
			hit++
		}
	}
	for k, lim := range limCPU {
		if peakCPU[k]*100 >= lim*95 {
			c.Details = append(c.Details, fmt.Sprintf("%s / %s: CPU peaked at its %dm limit (CPU is throttled at the limit, not killed)", k.comp.Label(), k.name, lim))
		}
	}
	if hit == 0 {
		c.Status, c.Summary = Pass, "Peak memory stayed under the limit for every Falcon container."
	} else {
		c.Status, c.Summary = Fail, fmt.Sprintf("%s reached its memory limit.", plural(hit, "container"))
	}
	return c
}

// checkCrowd: falconNodes maps node -> Falcon p95 CPU share of that node (%).
func checkCrowd(events []model.Event, falconNodes map[string]float64) Check {
	c := Check{ID: "crowd", Title: "Doesn't crowd workloads", Status: Pass,
		Summary: "No evictions or node pressure on nodes running Falcon."}
	review := 0
	for _, e := range events {
		share, onFalconNode := falconNodes[e.Node]
		switch {
		case e.Kind == model.EventEviction && e.Falcon:
			c.Status = Fail
			c.Details = append(c.Details, fmt.Sprintf("Falcon pod %s/%s was evicted from %s at %s.", e.Namespace, e.Object, e.Node, e.TS.UTC().Format(time.RFC3339)))
		case (e.Kind == model.EventEviction || e.Kind == model.EventPressure) && onFalconNode:
			review++
			c.Details = append(c.Details, fmt.Sprintf("%s on %s at %s (%s). Falcon used %.1f%% of this node's CPU at p95, so the cause is likely another workload.",
				e.Reason, e.Node, e.TS.UTC().Format(time.RFC3339), e.Object, share))
		}
	}
	switch {
	case c.Status == Fail:
		c.Summary = "A Falcon pod was evicted."
	case review > 0:
		c.Status, c.Summary = Review, fmt.Sprintf("%s on nodes running Falcon. Check whether Falcon contributed.", plural(review, "pressure or eviction event"))
	}
	return c
}

// checkCoverage walks the DaemonSet Sensor snapshot timeline and totals the
// time any sensor DaemonSet had fewer Ready pods than desired.
func checkCoverage(snaps []TimedDaemonSets, from, to time.Time) Check {
	c := Check{ID: "coverage", Title: "Full coverage"}
	if len(snaps) == 0 {
		c.Status, c.Summary = NotMeasured, "No DaemonSet Sensor found."
		return c
	}
	var short time.Duration
	for i, s := range snaps {
		start, end := s.TS, to
		if start.Before(from) {
			start = from
		}
		if i+1 < len(snaps) {
			end = snaps[i+1].TS
		}
		for _, d := range s.State {
			if d.Ready < d.Desired && end.After(start) {
				short += end.Sub(start)
				c.Details = append(c.Details, fmt.Sprintf("%s/%s: %d of %d Ready from %s for %s", d.Namespace, d.Name, d.Ready, d.Desired, start.UTC().Format(time.RFC3339), end.Sub(start)))
				break
			}
		}
	}
	last := snaps[len(snaps)-1].State
	var ready, desired int32
	for _, d := range last {
		ready += d.Ready
		desired += d.Desired
	}
	if short == 0 {
		c.Status, c.Summary = Pass, fmt.Sprintf("Every eligible node had a Ready sensor the whole time (%d/%d).", ready, desired)
	} else {
		c.Status, c.Summary = Fail, fmt.Sprintf("Sensor coverage dropped below 100%% for %s. Now %d/%d.", short, ready, desired)
	}
	return c
}

func checkAdmission(hooks []model.WebhookState, kacPresent bool) Check {
	c := Check{ID: "admission", Title: "Safe admission control"}
	if !kacPresent && len(hooks) == 0 {
		c.Status, c.Summary = NotMeasured, "KAC is not installed."
		return c
	}
	if len(hooks) == 0 {
		c.Status, c.Summary = NotMeasured, "KAC is running but no Falcon webhook configuration was found."
		return c
	}
	c.Status, c.Summary = Pass, "If KAC is unavailable, deployments continue (failurePolicy: Ignore)."
	for _, h := range hooks {
		c.Details = append(c.Details, fmt.Sprintf("%s (%s): failurePolicy %s, timeout %ds", h.Name, h.Kind, h.FailurePolicy, h.TimeoutSeconds))
		if h.FailurePolicy == "Fail" {
			c.Status = Review
			c.Summary = "A Falcon webhook uses failurePolicy: Fail. If KAC is down, matching requests are rejected until it recovers. This is a deliberate choice some teams make to enforce policy; confirm it is intended."
		}
	}
	return c
}

const staleAfter = time.Hour

func checkSensorHealth(hosts []model.SensorHost, known bool, to time.Time) Check {
	c := Check{ID: "sensor", Title: "Sensor healthy"}
	if !known {
		c.Status, c.Summary = NotMeasured, "Falcon API not enabled. Add an API client to see per-node sensor status."
		return c
	}
	bad := 0
	for _, h := range hosts {
		switch {
		case h.RFM:
			bad++
			c.Details = append(c.Details, fmt.Sprintf("%s: reduced functionality mode (sensor %s)", h.Hostname, h.Version))
		case !h.Online || to.Sub(h.LastSeen) > staleAfter:
			bad++
			c.Details = append(c.Details, fmt.Sprintf("%s: last seen %s", h.Hostname, h.LastSeen.UTC().Format(time.RFC3339)))
		}
	}
	if bad == 0 {
		c.Status, c.Summary = Pass, fmt.Sprintf("All %d sensors online, none in reduced functionality mode.", len(hosts))
	} else {
		c.Status, c.Summary = Fail, fmt.Sprintf("%s offline, stale, or in reduced functionality mode.", plural(bad, "sensor"))
	}
	return c
}

func MiB(b int64) string { return fmt.Sprintf("%dMi", b>>20) }
