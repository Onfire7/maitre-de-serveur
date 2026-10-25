#!/usr/bin/env bash
# One-time, needs sudo: give the development VM a real network interface on
# this machine instead of QEMU's NAT, so the Console discovers it over mDNS
# and <service>.local names resolve from the host.
#
# Wi-Fi cannot be bridged, so this creates a host-only bridge, maitre0, that
# NetworkManager runs in "shared" mode: it hands out DHCP leases (10.42.0.x)
# and NATs the VM to the internet through whatever uplink the host has.
# Then it lets QEMU's setuid bridge helper attach to that bridge.
#
# `make vm` uses the bridge automatically when it exists.
set -euo pipefail
BR=${BR:-maitre0}
if ! nmcli -t -f NAME con show | grep -qx "$BR"; then
  sudo nmcli con add type bridge ifname "$BR" con-name "$BR" ipv4.method shared ipv6.method ignore bridge.stp no
fi
sudo nmcli con up "$BR" >/dev/null
if ! grep -qx "allow $BR" /etc/qemu/bridge.conf 2>/dev/null; then
  echo "allow $BR" | sudo tee -a /etc/qemu/bridge.conf >/dev/null
fi
echo "bridge $BR is up: $(ip -4 -br addr show "$BR" | awk '{print $3}')"
echo "run 'make vm' now; the VM gets a 10.42.0.x address and shows it on its screen"
