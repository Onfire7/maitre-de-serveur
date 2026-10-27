// Browse the folders of a Space and make new ones (decision 175): used by
// Move and by every place a Service folder is picked.
import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ChevronRight, CornerLeftUp, Folder, FolderPlus } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";

export interface FolderSource {
  /** Folder names in `path` of the Space, on the Node browsed. */
  list: (path: string) => Promise<string[]>;
  /** Make `path` (its parent exists). */
  make?: (path: string) => Promise<void>;
  /** Changes when the Space or Node browsed changes. */
  key: string;
}

export function FolderBrowser({ source, top, path, onPath, disabled }: { source: FolderSource; top: string; path: string; onPath: (p: string) => void; disabled?: (p: string) => string | undefined }) {
  const qc = useQueryClient();
  const folders = useQuery({ queryKey: ["browse-folders", source.key, path], retry: false, queryFn: () => source.list(path) });
  const [naming, setNaming] = useState<string | null>(null);
  const crumbs = path.split("/").filter(Boolean);
  const join = (f: string) => [path, f].filter(Boolean).join("/");
  async function make() {
    const name = (naming ?? "").trim();
    if (!name || !source.make) return;
    if (name.includes("/") || name.startsWith(".")) return toast.error("A folder name cannot contain / or start with a dot");
    try {
      await source.make(join(name));
      setNaming(null);
      await qc.invalidateQueries({ queryKey: ["browse-folders", source.key] });
      onPath(join(name));
    } catch (e) {
      toast.error(String((e as Error).message ?? e));
    }
  }
  return (
    <div className="rounded-md border">
      <nav className="flex flex-wrap items-center gap-1 border-b px-2 py-1.5 text-sm">
        <button type="button" className="rounded px-1 font-medium hover:bg-accent" onClick={() => onPath("")}>
          {top}
        </button>
        {crumbs.map((c, i) => (
          <span key={i} className="flex items-center gap-1">
            <ChevronRight className="size-3.5 text-muted-foreground" />
            <button type="button" className="rounded px-1 hover:bg-accent" onClick={() => onPath(crumbs.slice(0, i + 1).join("/"))}>
              {c}
            </button>
          </span>
        ))}
        {source.make && naming === null && (
          <Button type="button" size="sm" variant="ghost" className="ml-auto h-7 text-xs" onClick={() => setNaming("")}>
            <FolderPlus /> New folder
          </Button>
        )}
      </nav>
      {naming !== null && (
        <div className="flex items-center gap-2 border-b px-2 py-1.5">
          <Input
            autoFocus
            aria-label="New folder name"
            value={naming}
            onChange={(e) => setNaming(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter") (e.preventDefault(), make());
              if (e.key === "Escape") (e.stopPropagation(), setNaming(null));
            }}
            placeholder="Folder name"
            className="h-7 text-sm"
          />
          <Button type="button" size="sm" className="h-7" disabled={!naming.trim()} onClick={make}>
            Make
          </Button>
          <Button type="button" size="sm" variant="ghost" className="h-7" onClick={() => setNaming(null)}>
            Cancel
          </Button>
        </div>
      )}
      <ul className="max-h-48 overflow-y-auto py-1 text-sm" aria-label="Folders">
        {crumbs.length > 0 && (
          <li>
            <button type="button" className="flex w-full items-center gap-2 px-3 py-1 text-left hover:bg-accent" onClick={() => onPath(crumbs.slice(0, -1).join("/"))}>
              <CornerLeftUp className="size-4 text-muted-foreground" /> ..
            </button>
          </li>
        )}
        {folders.error ? (
          <li className="px-3 py-1 text-xs text-muted-foreground">{crumbs.length ? "This folder does not exist yet; it is made when used." : "Cannot read this Space here."}</li>
        ) : !folders.data ? (
          <li className="px-3 py-1 text-xs text-muted-foreground">Reading…</li>
        ) : (
          folders.data.map((f) => {
            const why = disabled?.(join(f));
            return (
              <li key={f}>
                <button type="button" disabled={!!why} title={why} className="flex w-full items-center gap-2 px-3 py-1 text-left hover:bg-accent disabled:opacity-40" onClick={() => onPath(join(f))}>
                  <Folder className="size-4 text-status-starting" /> {f}
                </button>
              </li>
            );
          })
        )}
        {folders.data?.length === 0 && <li className="px-3 py-1 text-xs text-muted-foreground">No folders in here.</li>}
      </ul>
    </div>
  );
}
