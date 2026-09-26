# Environment inventory

The inventory is a YAML file describing the capture rig. It is the interface
between the hardware-bound work — injecting faults and capturing fixtures —
and everything else, which never touches hardware.

It is read by:

- **fault injection** (`hack/inject/`), to find a target disk and to refuse
  any disk not explicitly marked destructible;
- **capture** (`tropis capture`), to record what a fixture was captured on;
- **the eval runner** (`tropis eval --inventory`), to describe the rig in the
  report.

A complete example with placeholder values is at
[`hack/inventory.example.yaml`](../hack/inventory.example.yaml).

## Schema

```yaml
apiVersion: tropis.io/v1alpha1     # required, exactly this
kind: Inventory                    # required, exactly this

cluster:
  name: tropis-lab                 # required
  kubernetesVersion: v1.31.2       # required
  distribution: kubespray          # optional: kubespray, talos, ...
  containerRuntime: containerd://1.7.22   # optional

nodes:                             # at least one
  - name: worker-02                # required: the Kubernetes node name
    role: worker                   # required: control-plane or worker
    address: 192.0.2.12            # optional: where injection reaches the node
    kernel: 6.8.0-45-generic       # optional: uname -r
    osRelease: Ubuntu 24.04.1 LTS  # optional
    bmc:                           # optional; unused until Phase 6
      endpoint: https://192.0.2.112
      protocol: redfish            # redfish, ipmi, idrac or ilo
    disks:
      - id: sacrificial            # required: lower-case handle, unique per node
        device: /dev/sdb           # required: kernel device path
        byId: /dev/disk/by-id/ata-...   # optional, preferred for targeting
        model: Example HDD 2TB     # optional
        transport: sata            # optional: sata, nvme, sas or loop
        destructible: true         # default false; see below
        mountpoint: /mnt/disks/sacrificial  # optional; exhaustion and corruption need it
        notes: free text           # optional
```

Unknown fields are errors. A misspelt `destructable: true` fails to load
rather than leaving a disk protected in your head and unprotected in the code.

## `destructible`

Every injection script refuses any disk not explicitly marked
`destructible: true`. The field defaults to false, and there is deliberately
no command-line override: making a disk a target is an edit to this file,
reviewed like any other.

Validation also rejects a destructible disk whose `mountpoint` is a system
path (`/`, `/var`, `/var/lib/kubelet`, `/var/lib/containerd`, `/var/lib/etcd`
and similar). That combination is almost certainly a typo, and believing it
would mean a wiped node.

Injection targets `byId` when it is set, because kernel names such as
`/dev/sdb` can move between reboots and a stable path cannot.

## Querying it

```console
$ tropis inventory validate hack/inventory.example.yaml
inventory OK: cluster tropis-lab, 3 nodes, 1 destructible disk

$ tropis inventory target --inventory rig.yaml --node worker-02 --disk sacrificial
/dev/disk/by-id/ata-EXAMPLE_HDD_2TB_SERIAL0001

$ tropis inventory target --inventory rig.yaml --node worker-02 --disk system
tropis: disk is not marked destructible in the inventory: system on worker-02
```

`target` exits non-zero and prints nothing on stdout when it refuses. It is
what the injection scripts call, so the destructible check lives in one
place, in tested Go, rather than being re-implemented in shell.
