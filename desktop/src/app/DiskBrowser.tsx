// Look inside a disk of a machine being set up (decision 262): its
// partitions, and the folders of those the Installer can open, read-only,
// so the person can tell which disk is which before erasing one.
import { useEffect, useState } from "react";
import { Code, ConnectError, type Client } from "@connectrpc/connect";
import { ChevronRight, File, Folder, Link2, Loader2 } from "lucide-react";
import { cn } from "@/lib/utils";
import { formatBytes } from "@/lib/format";
import type { Disk, InstallerService, ListFolderResponse, Partition } from "@/gen/maitre/v1/installer_pb";

/** A filesystem type in words where blkid's name says little. */
export function fsName(fstype: string) {
  return ({ crypto_LUKS: "encrypted", BitLocker: "encrypted", LVM2_member: "LVM", linux_raid_member: "RAID" } as Record<string, string>)[fstype] ?? fstype;
}

/** What a disk has on it, in a few words: its partitions of a gigabyte or more. */
export function holds(d: Disk) {
  if (d.partitions.length === 0) return "Blank";
  const big = d.partitions
    .filter((p) => p.sizeBytes >= 1n << 30n)
    .map((p) => {
      const fs = fsName(p.fstype);
      return p.label && fs ? `${p.label} (${fs})` : p.label || fs;
    })
    .filter(Boolean);
  return big.length ? big.join(", ") : `${d.partitions.length} small partition${d.partitions.length === 1 ? "" : "s"}`;
}

/** Why a partition cannot be looked inside. */
export function closedReason(p: Partition) {
  switch (p.fstype) {
    case "":
      return "no filesystem";
    case "crypto_LUKS":
    case "BitLocker":
      return "encrypted";
    case "LVM2_member":
      return "LVM: its volumes are listed separately";
    case "linux_raid_member":
    case "zfs_member":
      return "part of a RAID set";
    case "swap":
      return "swap space";
  }
  return `cannot open ${p.fstype}`;
}

function partName(p: Partition) {
  return p.label || p.device.split("/").pop() || p.device;
}

/** The error in words; older Installers do not have ListFolder. */
function message(e: unknown) {
  const ce = ConnectError.from(e);
  if (ce.code === Code.Unimplemented) return "This Installer is too old to show what is on its disks. Installer media made from a newer build can.";
  return ce.rawMessage;
}

export function DiskBrowser({ client, disk }: { client: Client<typeof InstallerService>; disk: Disk }) {
  const first = disk.partitions.filter((p) => p.browsable).sort((a, b) => Number(b.sizeBytes - a.sizeBytes))[0];
  const [device, setDevice] = useState(first?.device ?? "");
  const [path, setPath] = useState<string[]>([]);
  const [resp, setResp] = useState<ListFolderResponse | null>(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);

  useEffect(() => {
    if (!device) return;
    let stop = false;
    setLoading(true);
    setError("");
    client
      .listFolder({ device, path: path.join("/") })
      .then((r) => !stop && setResp(r))
      .catch((e) => !stop && (setResp(null), setError(message(e))))
      .finally(() => !stop && setLoading(false));
    return () => {
      stop = true;
    };
  }, [client, device, path]);

  const part = disk.partitions.find((p) => p.device === device);
  return (
    <div className="space-y-2 rounded-lg border bg-muted/30 p-3 text-sm">
      <div className="flex flex-wrap gap-1.5">
        {disk.partitions.map((p) => (
          <button
            key={p.device}
            type="button"
            disabled={!p.browsable}
            title={p.browsable ? p.device : `${p.device}: ${closedReason(p)}`}
            onClick={() => (setDevice(p.device), setPath([]))}
            className={cn("rounded-md border px-2 py-1 text-xs", p.device === device ? "border-primary bg-primary text-primary-foreground" : "bg-background", !p.browsable && "cursor-not-allowed opacity-60")}
          >
            {partName(p)} <span className="opacity-70">{p.fstype && ` · ${fsName(p.fstype)}`} · {formatBytes(p.sizeBytes)}</span>
            {!p.browsable && <span className="opacity-70"> · {closedReason(p)}</span>}
          </button>
        ))}
      </div>
      {!part ? (
        <p className="text-xs text-muted-foreground">Nothing on this disk can be opened.</p>
      ) : (
        <>
          <div className="flex flex-wrap items-center gap-1 text-xs">
            <button type="button" className="font-medium hover:underline" onClick={() => setPath([])}>
              {partName(part)}
            </button>
            {path.map((seg, i) => (
              <span key={i} className="flex items-center gap-1">
                <ChevronRight className="size-3 text-muted-foreground" />
                <button type="button" className="hover:underline" onClick={() => setPath(path.slice(0, i + 1))}>
                  {seg}
                </button>
              </span>
            ))}
            {loading && <Loader2 className="ml-1 size-3 animate-spin text-muted-foreground" />}
            {resp && resp.totalBytes > 0n && (
              <span className="ml-auto text-muted-foreground">
                {formatBytes(resp.usedBytes)} of {formatBytes(resp.totalBytes)} used
              </span>
            )}
          </div>
          {error ? (
            <p className="text-xs text-destructive">{error}</p>
          ) : resp && resp.entries.length === 0 ? (
            <p className="text-xs text-muted-foreground">This folder is empty.</p>
          ) : (
            resp && (
              <ul className="max-h-64 divide-y overflow-auto rounded-md border bg-background">
                {resp.entries.map((e) => {
                  const open = e.dir && !e.link;
                  return (
                    <li key={e.name}>
                      <button type="button" disabled={!open} onClick={() => setPath([...path, e.name])} className={cn("flex w-full items-center gap-2 px-2 py-1 text-left text-xs", open ? "hover:bg-muted" : "cursor-default")}>
                        {e.link ? <Link2 className="size-3.5 shrink-0 text-muted-foreground" /> : e.dir ? <Folder className="size-3.5 shrink-0 text-muted-foreground" /> : <File className="size-3.5 shrink-0 text-muted-foreground" />}
                        <span className="min-w-0 flex-1 truncate">{e.name}</span>
                        {!e.dir && !e.link && <span className="shrink-0 tabular-nums text-muted-foreground">{formatBytes(e.sizeBytes)}</span>}
                        {e.modifiedUnixMs > 0n && <span className="w-24 shrink-0 text-right tabular-nums text-muted-foreground">{new Date(Number(e.modifiedUnixMs)).toLocaleDateString()}</span>}
                      </button>
                    </li>
                  );
                })}
              </ul>
            )
          )}
          {resp && resp.more > 0 && <p className="text-xs text-muted-foreground">and {resp.more} more not shown</p>}
        </>
      )}
      <p className="text-xs text-muted-foreground">Read-only: looking changes nothing on the disk.</p>
    </div>
  );
}
