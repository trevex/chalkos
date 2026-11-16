---
title: "chalkd's sandbox"
description: "The systemd sandbox of chalkd.service, what it leaves out and why"
---

# chalkd's sandbox

[A node runs its image from a read-only store](../../how-it-works/node/architecture.md#a-node-runs-its-image-from-a-read-only-store) explains why chalkd runs as root; this page lists the sandbox its unit keeps.

## What the unit leaves out

The unit `chalkd.service` is declared in `modules/node/chalkd.nix`. chalkd runs as root with its
capabilities, because it partitions disks, enrols TPM keyslots, writes EFI variables, mounts
volumes the host must see and sets the hostname. Its unit therefore has no file system or hostname
namespace. `ProtectKernelLogs` stays off too: systemd would give chalkd a mount namespace for it,
and chalkd's mounts would not reach the host.

## What the unit keeps

- `RestrictAddressFamilies` allows only the socket families chalkd uses: Unix, IPv4, IPv6,
  netlink, the kernel crypto API (`AF_ALG`, for cryptsetup and systemd-cryptenroll) and packet
  sockets (`AF_PACKET`, for the gratuitous ARP of a VIP).
- `NoNewPrivileges` forbids new privileges.
- `SystemCallArchitectures=native` fixes the system call architecture.
- `ProtectClock` protects the clock, and `LockPersonality` and `RestrictRealtime` are on.

`Restart=always` with no start limit restarts chalkd whenever it stops. chalkd is the only way to
reach the node, which has no SSH and no login.
