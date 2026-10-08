#!/usr/bin/env bash
# Creates the KVM guest scripts/ci-vm-gate.sh runs on. Run it once; it refuses
# to touch a VM or pool that already exists.
#
#   ./scripts/ci-vm-create.sh <ubuntu-24.04-cloud-image.img>
#
# The image is the Ubuntu 24.04 (noble) cloud image, the release the hosted
# runner's ubuntu-latest uses. It is copied, never modified: the guest disk is
# a qcow2 overlay on the copy. No root is needed: libvirt creates the pool
# directory under /var/lib/libvirt/images, owned by the calling user, for a
# member of the libvirt group.
#
# Sizing: 8 vCPU / 12 GB, a 40 GB thin disk (about 10-15 GB once the Go
# toolchain, module cache and build cache are warm).
set -euo pipefail

IMAGE=${1:?usage: ci-vm-create.sh <ubuntu-24.04-cloud-image.img>}
VM="${GATE_VM:-bentoolkit-ci}"
KEY="${GATE_VM_KEY:-$HOME/.ssh/ci_runner}"
CONN="qemu:///system"
DIR="/var/lib/libvirt/images/$VM"

[[ -f "$IMAGE" ]] || { echo "no such image: $IMAGE" >&2; exit 1; }
[[ -f "$KEY.pub" ]] || { echo "no public key $KEY.pub: ssh-keygen -t ed25519 -f $KEY" >&2; exit 1; }
if virsh --connect "$CONN" dominfo "$VM" >/dev/null 2>&1 ||
  virsh --connect "$CONN" pool-info "$VM" >/dev/null 2>&1; then
  echo "$VM already exists (domain or pool); remove it first to recreate it" >&2
  exit 1
fi

pool_xml=$(mktemp)
trap 'rm -f "$pool_xml"' EXIT
cat >"$pool_xml" <<XML
<pool type='dir'>
  <name>$VM</name>
  <target>
    <path>$DIR</path>
    <permissions><mode>0755</mode><owner>$(id -u)</owner><group>$(getent group libvirt | cut -d: -f3)</group></permissions>
  </target>
</pool>
XML
virsh --connect "$CONN" pool-define "$pool_xml" >/dev/null
virsh --connect "$CONN" pool-build "$VM" >/dev/null
virsh --connect "$CONN" pool-start "$VM" >/dev/null
virsh --connect "$CONN" pool-autostart "$VM" >/dev/null

base="$DIR/$(basename "$IMAGE")"
cp --reflink=auto "$IMAGE" "$base"
qemu-img create -q -f qcow2 -F qcow2 -b "$base" "$DIR/disk.qcow2" 40G

# dbus: the tray's D-Bus tests fail, never skip, under CI=true without it.
# bc: the coverage threshold check. golang-go only bootstraps: GOTOOLCHAIN
# then fetches the exact toolchain go.mod pins, as setup-go does.
cat >"$DIR/user-data" <<YAML
#cloud-config
hostname: $VM
users:
  - name: runner
    sudo: ALL=(ALL) NOPASSWD:ALL
    shell: /bin/bash
    ssh_authorized_keys:
      - $(cat "$KEY.pub")
package_update: true
packages: [git, make, build-essential, ca-certificates, curl, jq, bc, dbus, golang-go]
YAML
printf 'instance-id: %s\nlocal-hostname: %s\n' "$VM" "$VM" >"$DIR/meta-data"
cloud-localds "$DIR/seed.iso" "$DIR/user-data" "$DIR/meta-data"
virsh --connect "$CONN" pool-refresh "$VM" >/dev/null

virt-install --connect "$CONN" --name "$VM" --memory 12288 --vcpus 8 --cpu host-passthrough \
  --import --disk path="$DIR/disk.qcow2",format=qcow2,bus=virtio \
  --disk path="$DIR/seed.iso",device=cdrom \
  --network network=default,model=virtio --osinfo ubuntu24.04 --graphics none --noautoconsole
echo "$VM created; cloud-init finishes in a minute or two. Then: ./scripts/ci-vm-gate.sh"
