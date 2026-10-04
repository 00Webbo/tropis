// Package inventory describes the capture rig: which nodes exist, which disks
// they have, and — above all — which disks are safe to destroy.
//
// The inventory is the interface between hardware-bound work and everything
// else. Fault injection reads it to find a target and to refuse any target
// not explicitly marked destructible; capture reads it to record what a
// fixture was captured on; the eval report reads it to describe the rig.
//
// The schema is documented in docs/inventory.md, and an example with
// placeholder values lives at hack/inventory.example.yaml.
package inventory

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path"
	"regexp"
	"strings"

	"sigs.k8s.io/yaml"
)

// APIVersion and Kind identify an inventory document.
const (
	APIVersion = "tropis.io/v1alpha1"
	Kind       = "Inventory"
)

// Inventory is the whole rig.
type Inventory struct {
	APIVersion string  `json:"apiVersion"`
	Kind       string  `json:"kind"`
	Cluster    Cluster `json:"cluster"`
	Nodes      []Node  `json:"nodes"`
}

// Cluster describes the Kubernetes installation.
type Cluster struct {
	Name              string `json:"name"`
	KubernetesVersion string `json:"kubernetesVersion"`
	// Distribution is how the cluster was built, e.g. kubespray or talos.
	Distribution string `json:"distribution,omitempty"`
	// ContainerRuntime is e.g. containerd://1.7.22.
	ContainerRuntime string `json:"containerRuntime,omitempty"`
}

// Node is one machine.
type Node struct {
	// Name is the Kubernetes node name.
	Name string `json:"name"`
	// Role is control-plane or worker.
	Role string `json:"role"`
	// Address is where injection scripts reach the node, typically over SSH.
	Address string `json:"address,omitempty"`
	// Kernel is the running kernel release, as uname -r.
	Kernel string `json:"kernel,omitempty"`
	// OSRelease is the distribution string.
	OSRelease string `json:"osRelease,omitempty"`
	// BMC is the out-of-band controller. Unused until Phase 6, recorded now
	// so the rig description does not need revisiting.
	BMC *BMC `json:"bmc,omitempty"`
	// Disks lists the node's block devices.
	Disks []Disk `json:"disks"`
}

// BMC is an out-of-band management controller.
type BMC struct {
	Endpoint string `json:"endpoint"`
	// Protocol is redfish, ipmi, idrac or ilo.
	Protocol string `json:"protocol,omitempty"`
}

// Disk is one block device.
type Disk struct {
	// ID is a short, stable handle used to name the disk on the command line,
	// e.g. "sacrificial".
	ID string `json:"id"`
	// Device is the kernel device path, e.g. /dev/sdb. It may change across
	// reboots; ByID should not.
	Device string `json:"device"`
	// ByID is the /dev/disk/by-id path. Preferred for targeting when set.
	ByID string `json:"byId,omitempty"`
	// Model is the drive model.
	Model string `json:"model,omitempty"`
	// Transport is sata, nvme, sas or loop.
	Transport string `json:"transport,omitempty"`
	// Destructible marks the disk safe to destroy. It defaults to false, and
	// every injection script refuses a disk without it. There is deliberately
	// no way to override this from the command line.
	Destructible bool `json:"destructible"`
	// Mountpoint is where the disk's filesystem is mounted for workloads
	// under test, if anywhere. Exhaustion and corruption scenarios need it.
	Mountpoint string `json:"mountpoint,omitempty"`
	// Notes is free text.
	Notes string `json:"notes,omitempty"`
}

// Target returns the path injection should use: ByID when set, since kernel
// names can move between reboots, otherwise Device.
func (d Disk) Target() string {
	if d.ByID != "" {
		return d.ByID
	}
	return d.Device
}

// Load reads and validates an inventory file. Unknown fields are errors: a
// misspelt "destructable: true" must not silently leave a disk protected in
// the operator's mind and unprotected in the code, or the reverse.
func Load(file string) (*Inventory, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("read inventory: %w", err)
	}
	return Parse(data)
}

// Parse decodes and validates an inventory document.
func Parse(data []byte) (*Inventory, error) {
	var inv Inventory
	if err := yaml.UnmarshalStrict(bytes.TrimSpace(data), &inv); err != nil {
		return nil, fmt.Errorf("parse inventory: %w", err)
	}
	if err := inv.Validate(); err != nil {
		return nil, err
	}
	return &inv, nil
}

var (
	idPattern   = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)
	devPattern  = regexp.MustCompile(`^/dev/[A-Za-z0-9._/:+-]+$`)
	validRoles  = map[string]bool{"control-plane": true, "worker": true}
	validTransp = map[string]bool{"": true, "sata": true, "nvme": true, "sas": true, "loop": true}
)

// Validate checks the inventory for mistakes that would make injection target
// the wrong thing, reporting all of them.
func (inv *Inventory) Validate() error {
	var errs []string
	add := func(format string, a ...any) { errs = append(errs, fmt.Sprintf(format, a...)) }

	if inv.APIVersion != APIVersion {
		add("apiVersion must be %q, got %q", APIVersion, inv.APIVersion)
	}
	if inv.Kind != Kind {
		add("kind must be %q, got %q", Kind, inv.Kind)
	}
	if inv.Cluster.Name == "" {
		add("cluster.name is required")
	}
	if inv.Cluster.KubernetesVersion == "" {
		add("cluster.kubernetesVersion is required")
	}
	if len(inv.Nodes) == 0 {
		add("at least one node is required")
	}

	nodeNames := map[string]bool{}
	for i, n := range inv.Nodes {
		where := fmt.Sprintf("nodes[%d]", i)
		if n.Name == "" {
			add("%s.name is required", where)
		} else {
			where = fmt.Sprintf("node %q", n.Name)
			if nodeNames[n.Name] {
				add("%s appears more than once", where)
			}
			nodeNames[n.Name] = true
		}
		if !validRoles[n.Role] {
			add("%s: role must be control-plane or worker, got %q", where, n.Role)
		}

		ids, devices := map[string]bool{}, map[string]bool{}
		for j, d := range n.Disks {
			dw := fmt.Sprintf("%s disks[%d]", where, j)
			if !idPattern.MatchString(d.ID) {
				add("%s: id %q must be lower-case letters, digits and hyphens", dw, d.ID)
			}
			if ids[d.ID] {
				add("%s: id %q is used twice on this node", dw, d.ID)
			}
			ids[d.ID] = true
			if !devPattern.MatchString(d.Device) {
				add("%s: device %q must be a /dev path", dw, d.Device)
			}
			if devices[d.Device] {
				add("%s: device %q is listed twice on this node", dw, d.Device)
			}
			devices[d.Device] = true
			if d.ByID != "" && !strings.HasPrefix(d.ByID, "/dev/disk/by-id/") {
				add("%s: byId %q must be under /dev/disk/by-id/", dw, d.ByID)
			}
			if !validTransp[d.Transport] {
				add("%s: transport %q must be sata, nvme, sas or loop", dw, d.Transport)
			}
			if d.Mountpoint != "" && !path.IsAbs(d.Mountpoint) {
				add("%s: mountpoint %q must be absolute", dw, d.Mountpoint)
			}
			// A destructible disk mounted at a system path is almost
			// certainly a typo, and the consequence of believing it is a
			// wiped node.
			if d.Destructible && isSystemMount(d.Mountpoint) {
				add("%s: a destructible disk cannot be mounted at system path %q", dw, d.Mountpoint)
			}
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("invalid inventory:\n  - %s", strings.Join(errs, "\n  - "))
	}
	return nil
}

func isSystemMount(m string) bool {
	if m == "" {
		return false
	}
	clean := path.Clean(m)
	for _, sys := range []string{"/", "/boot", "/boot/efi", "/usr", "/var", "/etc", "/home", "/var/lib", "/var/lib/kubelet", "/var/lib/containerd", "/var/lib/etcd"} {
		if clean == sys {
			return true
		}
	}
	return false
}

// ErrNotDestructible is returned when injection asks for a protected disk.
var ErrNotDestructible = errors.New("disk is not marked destructible in the inventory")

// Node returns the named node.
func (inv *Inventory) Node(name string) (*Node, error) {
	for i := range inv.Nodes {
		if inv.Nodes[i].Name == name {
			return &inv.Nodes[i], nil
		}
	}
	return nil, fmt.Errorf("node %q is not in the inventory", name)
}

// Disk returns a disk on a node, by ID.
func (inv *Inventory) Disk(node, id string) (*Node, *Disk, error) {
	n, err := inv.Node(node)
	if err != nil {
		return nil, nil, err
	}
	for i := range n.Disks {
		if n.Disks[i].ID == id {
			return n, &n.Disks[i], nil
		}
	}
	return nil, nil, fmt.Errorf("disk %q is not in the inventory for node %q", id, node)
}

// InjectionTarget returns a disk injection may use, and refuses any disk not
// explicitly marked destructible.
func (inv *Inventory) InjectionTarget(node, id string) (*Node, *Disk, error) {
	n, d, err := inv.Disk(node, id)
	if err != nil {
		return nil, nil, err
	}
	if !d.Destructible {
		return nil, nil, fmt.Errorf("%w: %s on %s", ErrNotDestructible, id, node)
	}
	return n, d, nil
}
