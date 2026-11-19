// Package model holds the types shared by every falcon-footprint package.
// CPU is always millicores, memory always bytes.
package model

import "time"

type Component string

const (
	DaemonSetSensor Component = "daemonset-sensor"
	KAC             Component = "kac"
	IAR             Component = "iar"
	Injector        Component = "injector"
	Operator        Component = "operator"
	Self            Component = "self" // falcon-footprint itself, reported separately
)

// FalconComponents is the display order for Falcon (non-Self) components.
var FalconComponents = []Component{DaemonSetSensor, KAC, IAR, Injector, Operator}

var labels = map[Component]string{
	DaemonSetSensor: "DaemonSet Sensor",
	KAC:             "KAC",
	IAR:             "IAR",
	Injector:        "Container Sensor Injector",
	Operator:        "Falcon Operator",
	Self:            "Falcon Footprint",
}

func (c Component) Label() string {
	if l, ok := labels[c]; ok {
		return l
	}
	return string(c)
}

// Sample is one container's usage at one instant. For 5-minute rollups,
// CPUm/MemBytes are averages and CPUMaxm/MemMax the bucket maxima; for raw
// samples the max fields equal the value.
type Sample struct {
	TS                   time.Time
	Node, Namespace, Pod string
	Container            string
	Component            Component
	CPUm, MemBytes       int64
	CPUMaxm, MemMax      int64
}

type NodeSample struct {
	TS                  time.Time
	Node                string
	AllocCPUm, AllocMem int64
	UsedCPUm, UsedMem   int64
}

type EventKind string

const (
	EventRestart     EventKind = "restart"
	EventOOMKill     EventKind = "oomkill"
	EventCrashLoop   EventKind = "crashloop"
	EventEviction    EventKind = "eviction"
	EventPressure    EventKind = "pressure"
	EventReadyChange EventKind = "ready_change"
)

type Event struct {
	TS                time.Time
	Kind              EventKind
	Namespace, Object string
	Node              string
	Reason, Message   string
	Falcon            bool // object is a Falcon pod
}

type SnapshotKind string

const (
	SnapDaemonSets SnapshotKind = "daemonsets" // []DaemonSetState
	SnapWebhooks   SnapshotKind = "webhooks"   // []WebhookState
	SnapPodSpecs   SnapshotKind = "pod_specs"  // []ContainerSpec
	SnapFalconAPI  SnapshotKind = "falcon_api" // []SensorHost
)

type DaemonSetState struct {
	Namespace, Name         string
	Desired, Ready, Updated int32
}

type WebhookState struct {
	Name           string
	Kind           string // "mutating" | "validating"
	FailurePolicy  string // "Ignore" | "Fail"
	TimeoutSeconds int32
}

type ContainerSpec struct {
	Component                 Component
	Namespace, Pod, Container string
	Node                      string
	Image, Version            string // Version = image tag
	Backend                   string // "kernel" | "bpf" | "" (sensor only)
	PriorityClass             string
	ReqCPUm, ReqMem           int64
	LimCPUm, LimMem           int64 // 0 = no limit
}

type SensorHost struct {
	Hostname string
	Version  string
	Online   bool
	RFM      bool
	LastSeen time.Time
}
