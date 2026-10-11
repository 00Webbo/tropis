# Scenario workloads

Workloads run on the target node during a capture. Each pins itself to
`${TROPIS_NODE}` and, where it has storage, keeps it on the sacrificial disk
under `${TROPIS_MOUNT}` via a hostPath — so a fault on that disk reaches it
the way a failing disk reaches real data.

The exception is the two exhaustion workloads, `log-spam` and
`emptydir-filler`. They are the fault: each overruns its own
ephemeral-storage allowance (a container limit, an emptyDir `sizeLimit`)
until the kubelet evicts it. That storage is the kubelet's, on the boot
disk, so their writes are rate-limited and hard-capped, and they are applied
only after `hack/inject/ephemeral-exhaustion.sh start` has checked the node.

Apply with the variables substituted:

```sh
export TROPIS_NODE=worker-02 TROPIS_MOUNT=/mnt/disks/sacrificial
envsubst < postgres-hostpath.yaml | kubectl apply -f -
```

Everything lands in the `tropis-eval` namespace, which is deleted between
captures so no scenario inherits another's state.

The passwords in these manifests are placeholders for a disposable test
cluster, not secrets.
