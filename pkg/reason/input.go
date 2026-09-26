package reason

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/nathanwebb/tropis/pkg/host/smart"
	"github.com/nathanwebb/tropis/pkg/redact"
	"github.com/nathanwebb/tropis/pkg/schema"
)

// Request is everything BuildInput needs: raw captures, exactly as a live
// node or a fixture provides them.
type Request struct {
	Node        string
	Host        schema.HostCapture
	Kubernetes  schema.K8sCapture
	TriggeredBy []string
}

// RequestFromFixture builds a Request from a fixture. The fixture's NPD
// conditions are not carried over: NPD output is never evidence.
func RequestFromFixture(f *schema.Fixture, triggeredBy []string) Request {
	return Request{
		Node:        f.Node,
		Host:        f.Host,
		Kubernetes:  f.Kubernetes,
		TriggeredBy: triggeredBy,
	}
}

// AnalysisInput is the redacted, model-ready view of one node.
//
// Its fields are unexported so the only way to obtain a usable one is
// BuildInput, which redacts. A zero AnalysisInput is rejected by every method
// that would expose content.
type AnalysisInput struct {
	// node is the real node name, for the verdict. The document the model
	// reads carries the redacted form, which differs when nodes are named by
	// IP address.
	node       string
	doc        Document
	text       []byte
	digest     string
	refs       map[string]refInfo
	redactions map[string]int
}

type refInfo struct {
	source      string
	collectedAt time.Time
	lines       int // for logs: number of lines, so #Ln citations can be checked
}

// Document is the structure the model reads. Every citable item carries a
// Ref, and a verdict's evidence must cite Refs that exist here.
type Document struct {
	Node        string    `json:"node"`
	ObservedAt  time.Time `json:"observedAt"`
	TriggeredBy []string  `json:"triggeredBy,omitempty"`
	Host        HostDoc   `json:"host"`
	Kubernetes  K8sDoc    `json:"kubernetes"`
}

// HostDoc is the host layer: SMART only in v1.
type HostDoc struct {
	CollectedAt         time.Time       `json:"collectedAt"`
	PreviousCollectedAt *time.Time      `json:"previousCollectedAt,omitempty"`
	Devices             []DeviceDoc     `json:"devices"`
	Unreadable          []UnreadableDoc `json:"unreadable,omitempty"`
}

// DeviceDoc is one disk. The serial number is deliberately absent.
type DeviceDoc struct {
	Ref          string    `json:"ref"`
	Path         string    `json:"path"`
	Transport    string    `json:"transport"`
	Model        string    `json:"model,omitempty"`
	Firmware     string    `json:"firmware,omitempty"`
	RotationRPM  *int      `json:"rotationRpm,omitempty"`
	HealthPassed *bool     `json:"healthPassed,omitempty"`
	Current      Counters  `json:"current"`
	Previous     *Counters `json:"previous,omitempty"`
	Attributes   []AttrDoc `json:"attributes,omitempty"`
	Messages     []string  `json:"messages,omitempty"`
}

// Counters are the normalised SMART values that change over time. A nil
// field means the device does not report it, not that it is zero.
type Counters struct {
	TemperatureC        *int    `json:"temperatureC,omitempty"`
	PowerOnHours        *uint64 `json:"powerOnHours,omitempty"`
	ReallocatedSectors  *uint64 `json:"reallocatedSectors,omitempty"`
	PendingSectors      *uint64 `json:"pendingSectors,omitempty"`
	UncorrectableErrors *uint64 `json:"uncorrectableErrors,omitempty"`
	MediaErrors         *uint64 `json:"mediaErrors,omitempty"`
	PercentageUsed      *uint64 `json:"percentageUsed,omitempty"`
	AvailableSpare      *uint64 `json:"availableSpare,omitempty"`
	CriticalWarning     *uint64 `json:"criticalWarning,omitempty"`
	ErrorLogCount       *uint64 `json:"errorLogCount,omitempty"`
	SelfTestErrors      *uint64 `json:"selfTestErrors,omitempty"`
}

// AttrDoc is one SATA attribute.
type AttrDoc struct {
	Ref        string `json:"ref"`
	ID         int    `json:"id"`
	Name       string `json:"name"`
	Value      int    `json:"value"`
	Worst      int    `json:"worst"`
	Threshold  int    `json:"threshold"`
	WhenFailed string `json:"whenFailed,omitempty"`
	Raw        string `json:"raw"`
}

// UnreadableDoc is a device that could not be read. That the disk could not
// be queried is itself a finding.
type UnreadableDoc struct {
	Ref    string `json:"ref"`
	Path   string `json:"path"`
	Reason string `json:"reason"`
	Detail string `json:"detail,omitempty"`
}

// K8sDoc is the Kubernetes layer.
type K8sDoc struct {
	CollectedAt    time.Time      `json:"collectedAt"`
	NodeConditions []ConditionDoc `json:"nodeConditions,omitempty"`
	Pods           []PodDoc       `json:"pods"`
	Events         []EventDoc     `json:"events,omitempty"`
	Logs           []LogDoc       `json:"logs,omitempty"`
}

// ConditionDoc is a kubelet node condition. NPD conditions never appear.
type ConditionDoc struct {
	Ref           string    `json:"ref"`
	Type          string    `json:"type"`
	Status        string    `json:"status"`
	Reason        string    `json:"reason,omitempty"`
	Message       string    `json:"message,omitempty"`
	LastChangedAt time.Time `json:"lastChangedAt,omitempty"`
}

// PodDoc summarises a pod. Env vars, annotations and most of the spec are
// omitted: they rarely matter for diagnosis and are where secrets live.
type PodDoc struct {
	Ref        string         `json:"ref"`
	Namespace  string         `json:"namespace"`
	Name       string         `json:"name"`
	Phase      string         `json:"phase"`
	Reason     string         `json:"reason,omitempty"`
	StartedAt  *time.Time     `json:"startedAt,omitempty"`
	Containers []ContainerDoc `json:"containers"`
	Volumes    []VolumeDoc    `json:"volumes,omitempty"`
}

// ContainerDoc summarises one container's status and resources.
type ContainerDoc struct {
	Name            string            `json:"name"`
	Init            bool              `json:"init,omitempty"`
	Ready           bool              `json:"ready"`
	RestartCount    int32             `json:"restartCount"`
	State           string            `json:"state"`
	LastTermination string            `json:"lastTermination,omitempty"`
	Requests        map[string]string `json:"requests,omitempty"`
	Limits          map[string]string `json:"limits,omitempty"`
}

// VolumeDoc names a volume and where its data lives. Whether a pod's storage
// is on this node's disk is often the whole question.
type VolumeDoc struct {
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Source string `json:"source,omitempty"`
}

// EventDoc is one Kubernetes event.
type EventDoc struct {
	Ref     string    `json:"ref"`
	Time    time.Time `json:"time"`
	Type    string    `json:"type"`
	Reason  string    `json:"reason"`
	Object  string    `json:"object"`
	Message string    `json:"message"`
	Count   int32     `json:"count,omitempty"`
}

// LogDoc is a container log. Lines are cited as "<ref>#L<n>", 1-based.
type LogDoc struct {
	Ref   string   `json:"ref"`
	Lines []string `json:"lines"`
}

// BuildInput turns raw captures into a redacted AnalysisInput.
//
// It is deterministic: the same captures always produce byte-identical text
// and therefore the same digest, which is what lets a verdict be tied to its
// input and a fixture replay be reproducible.
func BuildInput(req Request) (*AnalysisInput, error) {
	if strings.TrimSpace(req.Node) == "" {
		return nil, fmt.Errorf("build input: node name is required")
	}

	in := &AnalysisInput{refs: map[string]refInfo{}}
	r := redact.New()

	host, err := in.buildHost(req.Host, r)
	if err != nil {
		return nil, err
	}
	k8s, err := in.buildK8s(req.Kubernetes, r)
	if err != nil {
		return nil, err
	}

	triggered := append([]string(nil), req.TriggeredBy...)
	sort.Strings(triggered)

	doc := Document{
		Node:        req.Node,
		ObservedAt:  observedAt(req),
		TriggeredBy: triggered,
		Host:        host,
		Kubernetes:  k8s,
	}

	// Redact every string in the document, in field order, before anything
	// is serialised. Redacting the serialised JSON instead would miss
	// secrets whose quotes had been escaped.
	redactStrings(reflect.ValueOf(&doc).Elem(), r)
	redactions := r.Summary()

	// Refs were registered before redaction. Re-key them through the same
	// Redactor — its pseudonyms are stable — so a citation of a redacted ref
	// resolves.
	refs := make(map[string]refInfo, len(in.refs))
	for k, v := range in.refs {
		refs[r.String(k)] = v
	}
	in.refs = refs

	text, err := json.MarshalIndent(doc, "", " ")
	if err != nil {
		return nil, fmt.Errorf("build input: %w", err)
	}
	sum := sha256.Sum256(text)

	in.node = req.Node
	in.doc = doc
	in.text = text
	in.digest = "sha256:" + hex.EncodeToString(sum[:])
	in.redactions = redactions
	return in, nil
}

func observedAt(req Request) time.Time {
	t := req.Host.CollectedAt
	if req.Kubernetes.CollectedAt.After(t) {
		t = req.Kubernetes.CollectedAt
	}
	return t.UTC()
}

func (in *AnalysisInput) addRef(ref, source string, at time.Time, lines int) {
	in.refs[ref] = refInfo{source: source, collectedAt: at, lines: lines}
}

func (in *AnalysisInput) buildHost(hc schema.HostCapture, r *redact.Redactor) (HostDoc, error) {
	doc := HostDoc{CollectedAt: hc.CollectedAt.UTC(), Devices: []DeviceDoc{}}

	previous := map[string]*smart.Device{}
	if hc.Previous != nil {
		t := hc.Previous.CollectedAt.UTC()
		doc.PreviousCollectedAt = &t
		for path, raw := range hc.Previous.SMART {
			if d, fail := smart.Parse(raw, path); fail == nil {
				previous[deviceIdentity(d)] = d
			}
		}
	}

	paths := make([]string, 0, len(hc.SMART))
	for p := range hc.SMART {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	for _, path := range paths {
		d, fail := smart.Parse(hc.SMART[path], path)
		if fail != nil {
			ref := "smart:" + fail.Path
			in.addRef(ref, "smartctl", doc.CollectedAt, 0)
			doc.Unreadable = append(doc.Unreadable, UnreadableDoc{
				Ref: ref, Path: fail.Path, Reason: string(fail.Reason), Detail: fail.Detail,
			})
			continue
		}
		// Serials identify a physical asset; they are needed to match
		// readings, not to diagnose, so they never enter the document. They
		// are registered as literals in case one appears in free text.
		r.AddLiteral(d.SerialNumber, "serial")

		ref := "smart:" + d.Path
		in.addRef(ref, "smartctl", d.CollectedAt, 0)
		dd := DeviceDoc{
			Ref:          ref,
			Path:         d.Path,
			Transport:    string(d.Transport),
			Model:        d.ModelName,
			Firmware:     d.FirmwareVersion,
			RotationRPM:  d.RotationRate,
			HealthPassed: d.HealthPassed,
			Current:      counters(d),
		}
		if p, ok := previous[deviceIdentity(d)]; ok {
			c := counters(p)
			dd.Previous = &c
		}
		for _, a := range d.Attributes {
			aref := fmt.Sprintf("%s#%d", ref, a.ID)
			in.addRef(aref, "smartctl", d.CollectedAt, 0)
			raw := a.RawString
			if raw == "" {
				raw = strconv.FormatUint(a.RawValue, 10)
			}
			dd.Attributes = append(dd.Attributes, AttrDoc{
				Ref: aref, ID: a.ID, Name: a.Name, Value: a.Value, Worst: a.Worst,
				Threshold: a.Threshold, WhenFailed: a.WhenFailed, Raw: raw,
			})
		}
		for _, m := range d.Messages {
			dd.Messages = append(dd.Messages, m.Severity+": "+m.Text)
		}
		doc.Devices = append(doc.Devices, dd)
	}
	return doc, nil
}

// deviceIdentity matches a device across readings by model and serial, so a
// replaced drive at the same path is not compared with its predecessor.
func deviceIdentity(d *smart.Device) string {
	if d.SerialNumber != "" {
		return d.ModelName + "/" + d.SerialNumber
	}
	return "path:" + d.Path
}

func counters(d *smart.Device) Counters {
	return Counters{
		TemperatureC:        d.TemperatureCelsius,
		PowerOnHours:        d.PowerOnHours,
		ReallocatedSectors:  d.ReallocatedSectors,
		PendingSectors:      d.PendingSectors,
		UncorrectableErrors: d.UncorrectableErrors,
		MediaErrors:         d.MediaErrors,
		PercentageUsed:      d.PercentageUsed,
		AvailableSpare:      d.AvailableSpare,
		CriticalWarning:     d.CriticalWarning,
		ErrorLogCount:       d.ErrorLogCount,
		SelfTestErrors:      d.SelfTestErrors,
	}
}

func (in *AnalysisInput) buildK8s(kc schema.K8sCapture, r *redact.Redactor) (K8sDoc, error) {
	at := kc.CollectedAt.UTC()
	doc := K8sDoc{CollectedAt: at, Pods: []PodDoc{}}

	if !kc.NodeJSON.IsEmpty() {
		var node corev1.Node
		if err := json.Unmarshal(kc.NodeJSON, &node); err != nil {
			return doc, fmt.Errorf("build input: parse node: %w", err)
		}
		// Only kubelet conditions. The collector already split NPD's off;
		// this is the second line of defence, since a fixture may be
		// hand-assembled.
		for _, c := range node.Status.Conditions {
			if !isKubeletCondition(c.Type) {
				continue
			}
			ref := "node:condition/" + string(c.Type)
			in.addRef(ref, "node-conditions", at, 0)
			doc.NodeConditions = append(doc.NodeConditions, ConditionDoc{
				Ref: ref, Type: string(c.Type), Status: string(c.Status), Reason: c.Reason,
				Message: c.Message, LastChangedAt: c.LastTransitionTime.UTC(),
			})
		}
	}

	for i, raw := range kc.Pods {
		var p corev1.Pod
		if err := json.Unmarshal(raw, &p); err != nil {
			return doc, fmt.Errorf("build input: parse pod %d: %w", i, err)
		}
		ref := "pod:" + p.Namespace + "/" + p.Name
		in.addRef(ref, "pod-status", at, 0)
		doc.Pods = append(doc.Pods, podDoc(ref, &p))
	}
	sort.Slice(doc.Pods, func(i, j int) bool { return doc.Pods[i].Ref < doc.Pods[j].Ref })

	for i, raw := range kc.Events {
		var e corev1.Event
		if err := json.Unmarshal(raw, &e); err != nil {
			return doc, fmt.Errorf("build input: parse event %d: %w", i, err)
		}
		id := string(e.UID)
		if id == "" {
			id = e.Namespace + "/" + e.Name
		}
		if id == "/" {
			id = strconv.Itoa(i)
		}
		ref := "event:" + id
		in.addRef(ref, "kubelet-events", at, 0)
		doc.Events = append(doc.Events, EventDoc{
			Ref:     ref,
			Time:    eventTime(e).UTC(),
			Type:    e.Type,
			Reason:  e.Reason,
			Object:  e.InvolvedObject.Kind + " " + strings.TrimPrefix(e.InvolvedObject.Namespace+"/"+e.InvolvedObject.Name, "/"),
			Message: e.Message,
			Count:   e.Count,
		})
	}
	sort.SliceStable(doc.Events, func(i, j int) bool { return doc.Events[i].Time.Before(doc.Events[j].Time) })

	keys := make([]string, 0, len(kc.Logs))
	for k := range kc.Logs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		// Redact the log whole before splitting it into lines: a secret
		// spanning lines, such as a PEM block, only matches as a whole.
		lines := strings.Split(strings.TrimRight(r.String(kc.Logs[k]), "\n"), "\n")
		ref := "log:" + k
		in.addRef(ref, "pod-logs", at, len(lines))
		doc.Logs = append(doc.Logs, LogDoc{Ref: ref, Lines: lines})
	}
	return doc, nil
}

func isKubeletCondition(t corev1.NodeConditionType) bool {
	switch t {
	case corev1.NodeReady, corev1.NodeMemoryPressure, corev1.NodeDiskPressure,
		corev1.NodePIDPressure, corev1.NodeNetworkUnavailable:
		return true
	}
	return false
}

func eventTime(e corev1.Event) time.Time {
	for _, t := range []time.Time{e.LastTimestamp.Time, e.EventTime.Time, e.FirstTimestamp.Time, e.CreationTimestamp.Time} {
		if !t.IsZero() {
			return t
		}
	}
	return time.Time{}
}

func podDoc(ref string, p *corev1.Pod) PodDoc {
	pd := PodDoc{
		Ref:       ref,
		Namespace: p.Namespace,
		Name:      p.Name,
		Phase:     string(p.Status.Phase),
		Reason:    p.Status.Reason,
	}
	if p.Status.StartTime != nil {
		t := p.Status.StartTime.UTC()
		pd.StartedAt = &t
	}

	specs := map[string]corev1.Container{}
	for _, c := range p.Spec.InitContainers {
		specs["init/"+c.Name] = c
	}
	for _, c := range p.Spec.Containers {
		specs[c.Name] = c
	}

	add := func(s corev1.ContainerStatus, init bool) {
		key := s.Name
		if init {
			key = "init/" + s.Name
		}
		spec := specs[key]
		pd.Containers = append(pd.Containers, ContainerDoc{
			Name:            s.Name,
			Init:            init,
			Ready:           s.Ready,
			RestartCount:    s.RestartCount,
			State:           describeState(s.State),
			LastTermination: describeTermination(s.LastTerminationState.Terminated),
			Requests:        quantities(spec.Resources.Requests),
			Limits:          quantities(spec.Resources.Limits),
		})
	}
	for _, s := range p.Status.InitContainerStatuses {
		add(s, true)
	}
	for _, s := range p.Status.ContainerStatuses {
		add(s, false)
	}
	// A pod that has not started has specs but no statuses yet.
	if len(pd.Containers) == 0 {
		for _, c := range p.Spec.Containers {
			pd.Containers = append(pd.Containers, ContainerDoc{
				Name: c.Name, State: "not started",
				Requests: quantities(c.Resources.Requests), Limits: quantities(c.Resources.Limits),
			})
		}
	}

	for _, v := range p.Spec.Volumes {
		pd.Volumes = append(pd.Volumes, volumeDoc(v))
	}
	return pd
}

func describeState(s corev1.ContainerState) string {
	switch {
	case s.Waiting != nil:
		return strings.TrimSpace("waiting: " + s.Waiting.Reason + " " + s.Waiting.Message)
	case s.Terminated != nil:
		return "terminated: " + describeTermination(s.Terminated)
	case s.Running != nil:
		return "running since " + s.Running.StartedAt.UTC().Format(time.RFC3339)
	default:
		return "unknown"
	}
}

func describeTermination(t *corev1.ContainerStateTerminated) string {
	if t == nil {
		return ""
	}
	out := fmt.Sprintf("%s exit %d", t.Reason, t.ExitCode)
	if t.Signal != 0 {
		out += fmt.Sprintf(" signal %d", t.Signal)
	}
	if !t.FinishedAt.IsZero() {
		out += " at " + t.FinishedAt.UTC().Format(time.RFC3339)
	}
	if t.Message != "" {
		out += ": " + t.Message
	}
	return strings.TrimSpace(out)
}

func quantities(rl corev1.ResourceList) map[string]string {
	if len(rl) == 0 {
		return nil
	}
	out := make(map[string]string, len(rl))
	for k, v := range rl {
		out[string(k)] = v.String()
	}
	return out
}

// volumeDoc records a volume's kind and, where it identifies storage, its
// source. Secret and ConfigMap volumes are named by kind only.
func volumeDoc(v corev1.Volume) VolumeDoc {
	d := VolumeDoc{Name: v.Name}
	switch {
	case v.HostPath != nil:
		d.Kind, d.Source = "hostPath", v.HostPath.Path
	case v.PersistentVolumeClaim != nil:
		d.Kind, d.Source = "persistentVolumeClaim", v.PersistentVolumeClaim.ClaimName
	case v.EmptyDir != nil:
		d.Kind = "emptyDir"
		if v.EmptyDir.Medium == corev1.StorageMediumMemory {
			d.Source = "memory"
		}
	case v.Secret != nil:
		d.Kind = "secret"
	case v.ConfigMap != nil:
		d.Kind = "configMap"
	case v.Projected != nil:
		d.Kind = "projected"
	case v.CSI != nil:
		d.Kind, d.Source = "csi", v.CSI.Driver
	case v.NFS != nil:
		d.Kind = "nfs"
	default:
		d.Kind = "other"
	}
	return d
}

// redactStrings walks v and redacts every string it reaches: struct fields,
// slices, pointers and both keys and values of string maps. Walking the
// whole document means a field added later cannot bypass redaction.
func redactStrings(v reflect.Value, r *redact.Redactor) {
	switch v.Kind() {
	case reflect.String:
		if v.CanSet() {
			v.SetString(r.String(v.String()))
		}
	case reflect.Pointer:
		if !v.IsNil() {
			redactStrings(v.Elem(), r)
		}
	case reflect.Struct:
		if v.Type() == reflect.TypeOf(time.Time{}) {
			return
		}
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).IsExported() {
				redactStrings(v.Field(i), r)
			}
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			redactStrings(v.Index(i), r)
		}
	case reflect.Map:
		if v.IsNil() {
			return
		}
		keys := v.MapKeys()
		sort.Slice(keys, func(i, j int) bool { return fmt.Sprint(keys[i]) < fmt.Sprint(keys[j]) })
		clean := reflect.MakeMapWithSize(v.Type(), v.Len())
		for _, k := range keys {
			nk := reflect.New(k.Type()).Elem()
			nk.Set(k)
			redactStrings(nk, r)
			nv := reflect.New(v.Type().Elem()).Elem()
			nv.Set(v.MapIndex(k))
			redactStrings(nv, r)
			clean.SetMapIndex(nk, nv)
		}
		v.Set(clean)
	}
}

// Built reports whether the input came from BuildInput.
func (in *AnalysisInput) Built() bool { return in != nil && in.text != nil }

// Node is the real name of the node under analysis.
func (in *AnalysisInput) Node() string { return in.node }

// ObservedAt is when the signals were collected.
func (in *AnalysisInput) ObservedAt() time.Time { return in.doc.ObservedAt }

// TriggeredBy lists the pre-filter rules that raised the node.
func (in *AnalysisInput) TriggeredBy() []string { return append([]string(nil), in.doc.TriggeredBy...) }

// Digest is the SHA-256 of the exact redacted text a model receives.
func (in *AnalysisInput) Digest() string { return in.digest }

// Document returns a copy of the redacted document, for backends that reason
// over structure rather than text (the mock backend).
func (in *AnalysisInput) Document() (Document, error) {
	if !in.Built() {
		return Document{}, ErrUnbuiltInput
	}
	var cp Document
	if err := json.Unmarshal(in.text, &cp); err != nil {
		return Document{}, err
	}
	return cp, nil
}

// Text returns the redacted document as the JSON a model receives.
func (in *AnalysisInput) Text() ([]byte, error) {
	if !in.Built() {
		return nil, ErrUnbuiltInput
	}
	return append([]byte(nil), in.text...), nil
}

// Redactions reports how many redactions of each kind were made, for audit
// logging. Counts only, never values.
func (in *AnalysisInput) Redactions() map[string]int {
	out := make(map[string]int, len(in.redactions))
	for k, v := range in.redactions {
		out[k] = v
	}
	return out
}

// lookupRef resolves a citation. Log citations may carry a "#L<n>" suffix,
// checked against the log's length.
func (in *AnalysisInput) lookupRef(ref string) (refInfo, bool) {
	if info, ok := in.refs[ref]; ok {
		return info, true
	}
	if base, line, ok := strings.Cut(ref, "#L"); ok {
		info, found := in.refs[base]
		if !found || info.source != "pod-logs" {
			return refInfo{}, false
		}
		n, err := strconv.Atoi(line)
		if err != nil || n < 1 || n > info.lines {
			return refInfo{}, false
		}
		return info, true
	}
	return refInfo{}, false
}
