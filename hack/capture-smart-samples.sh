#!/bin/sh
# Regenerate the captured smartctl samples in pkg/host/smart/testdata.
#
# These are real `smartctl -j` output, not reconstructions. Capturing them
# needs block devices and elevated privileges, so this runs in a privileged
# container:
#
#   docker run --rm --privileged -v /dev:/dev -v /lib/modules:/lib/modules:ro \
#     -v "$PWD:/repo" alpine:3.20 sh /repo/hack/capture-smart-samples.sh
#
# The NVMe sample is produced against a real NVMe controller created with the
# kernel's nvme-loop target, so smartctl exercises its genuine NVMe code path
# rather than a simulation. The SCSI sample uses scsi_debug.
#
# SATA attribute tables need physical SATA hardware and cannot be captured this
# way; sata-healthy.json and sata-failing.json are constructed to match the
# structure emitted by ataPrintSmartAttribWithThres in smartmontools 7.4. See
# the testdata README.
set -e

OUT="${OUT:-/repo/pkg/host/smart/testdata}"
mkdir -p "$OUT"

apk add --no-cache smartmontools nvme-cli >/dev/null 2>&1 || true

# --- Error paths -----------------------------------------------------------
# smartctl emits valid JSON with a non-zero exit status for all of these, which
# is exactly why the parser reads the envelope rather than the process exit
# code.

smartctl -j -a /dev/nonexistent0 > "$OUT/error-missing-device.json" 2>&1 || true

adduser -D -u 1001 probe 2>/dev/null || true
su probe -c "smartctl -j -a /dev/sda" > "$OUT/error-permission-denied.json" 2>&1 || true

smartctl -j -a -d nvme /dev/sda > "$OUT/error-wrong-devtype.json" 2>&1 || true

# A device present but reporting no SMART data at all.
smartctl -j -a /dev/sda > "$OUT/scsi-no-smart.json" 2>&1 || true

# --- SCSI/SAS --------------------------------------------------------------
modprobe scsi_debug dev_size_mb=64 2>/dev/null || true
sleep 2
for d in /dev/sd?; do
  if smartctl -j -i "$d" 2>/dev/null | grep -q '"scsi_vendor": *"Linux"'; then
    smartctl -j -a "$d" > "$OUT/scsi-sas-healthy.json" 2>&1 || true
    echo "captured SCSI sample from $d"
    break
  fi
done

# --- NVMe ------------------------------------------------------------------
modprobe nvmet nvme-loop 2>/dev/null || true
mount -t configfs none /sys/kernel/config 2>/dev/null || true

truncate -s 512M /tmp/nvme-backing.img
NQN=nqn.2026-09.io.tropis:dev1
D=/sys/kernel/config/nvmet/subsystems/$NQN

mkdir -p "$D"
echo 1 > "$D/attr_allow_any_host"
mkdir -p "$D/namespaces/1"
echo -n /tmp/nvme-backing.img > "$D/namespaces/1/device_path"
echo 1 > "$D/namespaces/1/enable"

mkdir -p /sys/kernel/config/nvmet/ports/1
echo -n loop > /sys/kernel/config/nvmet/ports/1/addr_trtype
ln -s "$D" /sys/kernel/config/nvmet/ports/1/subsystems/ 2>/dev/null || true

nvme connect -t loop -n "$NQN" >/dev/null 2>&1 || true
sleep 2

for d in /dev/nvme0n1 /dev/nvme1n1; do
  if [ -e "$d" ]; then
    smartctl -j -a "$d" > "$OUT/nvme-healthy.json" 2>&1 || true
    echo "captured NVMe sample from $d"
    break
  fi
done

echo "samples written to $OUT"
