#!/usr/bin/env bash
# One-time, needs sudo: put the development VM on the real LAN by bridging
# this machine's wired interface (decision 87). The VM then gets an address
# from the same DHCP server as every other machine, the Console discovers it
# over mDNS, <service>.local names resolve from the host, and the Depot's
# netboot answers it like a physical machine.
#
# NetworkManager moves the IP configuration from the NIC onto the bridge
# maitre0 (keeping the NIC's MAC so the DHCP lease stays), which drops the
# connection for a few seconds once. Then QEMU's setuid bridge helper is
# allowed to attach to the bridge. `make vm` uses it automatically.
#
#   scripts/vm-net-setup.sh            # bridges the wired NIC NetworkManager has connected
#   NIC=enp3s0 scripts/vm-net-setup.sh # pick the interface yourself
set -euo pipefail
BR=${BR:-maitre0}
if [ -z "${NIC:-}" ]; then
  NIC=$(nmcli -t -f DEVICE,TYPE,STATE device status | awk -F: '$2=="ethernet" && $3=="connected" {print $1; exit}')
  [ -n "$NIC" ] || { echo "vm-net-setup: no connected wired interface found (set NIC=...)"; exit 1; }
fi
if nmcli -t -f DEVICE,TYPE device status | grep -qx "$NIC:bridge"; then
  echo "vm-net-setup: $NIC is already a bridge"; exit 1
fi
MAC=$(cat "/sys/class/net/$NIC/address")
echo "bridging $NIC ($MAC) into $BR; the connection drops for a few seconds"
# An earlier version of this script made a host-only shared bridge; replace it.
if nmcli -t -f NAME con show | grep -qx "$BR"; then sudo nmcli con delete "$BR" >/dev/null; fi
if nmcli -t -f NAME con show | grep -qx "$BR-$NIC"; then sudo nmcli con delete "$BR-$NIC" >/dev/null; fi
sudo nmcli con add type bridge ifname "$BR" con-name "$BR" bridge.stp no bridge.mac-address "$MAC" \
  ipv4.method auto ipv6.method auto connection.autoconnect yes >/dev/null
sudo nmcli con add type ethernet ifname "$NIC" con-name "$BR-$NIC" master "$BR" connection.autoconnect yes >/dev/null
sudo nmcli con up "$BR" >/dev/null
if ! grep -qx "allow $BR" /etc/qemu/bridge.conf 2>/dev/null; then
  echo "allow $BR" | sudo tee -a /etc/qemu/bridge.conf >/dev/null
fi
for _ in $(seq 1 30); do
  ADDR=$(ip -4 -br addr show "$BR" | awk '{print $3}')
  [ -n "$ADDR" ] && break; sleep 1
done
echo "bridge $BR is up on $NIC: ${ADDR:-no address yet}"
echo "run 'make vm' now; the VM gets an address from your router and shows it on its screen"
echo "to undo: sudo nmcli con delete $BR $BR-$NIC  (the old $NIC connection reconnects by itself)"
