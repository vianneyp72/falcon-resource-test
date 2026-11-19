package analyze

import (
	"strings"
	"testing"
	"time"

	"github.com/vianneyp72/falcon-resource-test/internal/model"
)

var t0 = time.Unix(1_700_000_100, 0)

func TestCheckStable(t *testing.T) {
	if c := checkStable(nil, true); c.Status != Pass {
		t.Fatalf("no events: %+v", c)
	}
	ev := []model.Event{{Kind: model.EventRestart, Falcon: true, Object: "p1"}, {Kind: model.EventRestart, Falcon: false}}
	if c := checkStable(ev, true); c.Status != Fail || !strings.Contains(c.Summary, "1 restart") {
		t.Fatalf("one falcon restart: %+v", c)
	}
	if c := checkStable(nil, false); c.Status != NotMeasured {
		t.Fatalf("no falcon pods: %+v", c)
	}
}

func TestCheckLane(t *testing.T) {
	specs := []model.ContainerSpec{{Component: model.KAC, Container: "falcon-ac", LimMem: 256 << 20}}
	under := []model.Sample{{Component: model.KAC, Container: "falcon-ac", MemMax: 200 << 20}}
	at := []model.Sample{{Component: model.KAC, Container: "falcon-ac", MemMax: 256 << 20}}
	if c := checkLane(specs, under); c.Status != Pass {
		t.Fatalf("under: %+v", c)
	}
	if c := checkLane(specs, at); c.Status != Fail {
		t.Fatalf("at limit: %+v", c)
	}
	if c := checkLane([]model.ContainerSpec{{Component: model.KAC, Container: "falcon-ac"}}, under); c.Status != Info {
		t.Fatalf("no limits: %+v", c)
	}
	if c := checkLane(specs, nil); c.Status != NotMeasured {
		t.Fatalf("no samples: %+v", c)
	}
}

func TestCheckCrowd(t *testing.T) {
	falconNodes := map[string]float64{"n1": 1.5}
	if c := checkCrowd(nil, falconNodes); c.Status != Pass {
		t.Fatalf("clean: %+v", c)
	}
	evict := []model.Event{{Kind: model.EventEviction, Node: "n1", Falcon: true, Object: "falcon-sensor-x"}}
	if c := checkCrowd(evict, falconNodes); c.Status != Fail {
		t.Fatalf("falcon evicted: %+v", c)
	}
	press := []model.Event{{Kind: model.EventPressure, Node: "n1", Reason: "MemoryPressure"}}
	c := checkCrowd(press, falconNodes)
	if c.Status != Review || !strings.Contains(strings.Join(c.Details, " "), "1.5%") {
		t.Fatalf("pressure: %+v", c)
	}
	// pressure on a node without Falcon is not our concern
	if c := checkCrowd([]model.Event{{Kind: model.EventPressure, Node: "n9"}}, falconNodes); c.Status != Pass {
		t.Fatalf("other node: %+v", c)
	}
}

func TestCheckCoverage(t *testing.T) {
	to := t0.Add(10 * time.Minute)
	full := []TimedDaemonSets{{TS: t0, State: []model.DaemonSetState{{Name: "falcon-sensor", Desired: 3, Ready: 3}}}}
	if c := checkCoverage(full, t0, to); c.Status != Pass {
		t.Fatalf("full: %+v", c)
	}
	short := append(full, TimedDaemonSets{TS: t0.Add(4 * time.Minute), State: []model.DaemonSetState{{Name: "falcon-sensor", Desired: 3, Ready: 2}}},
		TimedDaemonSets{TS: t0.Add(6 * time.Minute), State: []model.DaemonSetState{{Name: "falcon-sensor", Desired: 3, Ready: 3}}})
	c := checkCoverage(short, t0, to)
	if c.Status != Fail || !strings.Contains(c.Summary, "2m0s") {
		t.Fatalf("2 minute shortfall: %+v", c)
	}
	if c := checkCoverage(nil, t0, to); c.Status != NotMeasured {
		t.Fatalf("no ds: %+v", c)
	}
}

func TestCheckAdmission(t *testing.T) {
	if c := checkAdmission(nil, false); c.Status != NotMeasured {
		t.Fatalf("no kac: %+v", c)
	}
	ign := []model.WebhookState{{Name: "validating.falcon-kac.crowdstrike.com", FailurePolicy: "Ignore", TimeoutSeconds: 10}}
	if c := checkAdmission(ign, true); c.Status != Pass {
		t.Fatalf("ignore: %+v", c)
	}
	fail := []model.WebhookState{{Name: "x", FailurePolicy: "Fail", TimeoutSeconds: 10}}
	if c := checkAdmission(fail, true); c.Status != Review {
		t.Fatalf("fail policy is review, not fail: %+v", c)
	}
}

func TestCheckSensorHealth(t *testing.T) {
	to := t0
	if c := checkSensorHealth(nil, false, to); c.Status != NotMeasured {
		t.Fatalf("not enabled: %+v", c)
	}
	ok := []model.SensorHost{{Hostname: "n1", Online: true, LastSeen: to.Add(-time.Minute)}}
	if c := checkSensorHealth(ok, true, to); c.Status != Pass {
		t.Fatalf("healthy: %+v", c)
	}
	rfm := []model.SensorHost{{Hostname: "n1", Online: true, RFM: true, LastSeen: to}}
	if c := checkSensorHealth(rfm, true, to); c.Status != Fail {
		t.Fatalf("rfm: %+v", c)
	}
	stale := []model.SensorHost{{Hostname: "n1", Online: true, LastSeen: to.Add(-2 * time.Hour)}}
	if c := checkSensorHealth(stale, true, to); c.Status != Fail {
		t.Fatalf("stale: %+v", c)
	}
}
