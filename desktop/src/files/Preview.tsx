// The file manager's preview (decisions 178-180): a side panel beside the
// list, or the whole window, with previous and next through the folder's
// files. What the web view cannot show says so and offers Download.
import { lazy, Suspense, useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { ChevronLeft, ChevronRight, Download, FileArchive, FileQuestion, Maximize2, Minimize2, PackageOpen, X } from "lucide-react";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import { formatBytes, ago } from "@/lib/format";
import type { Entry } from "@/gen/maitre/v1/files_pb";
import { contentUrl, type FileRef } from "./content";
import { kindOf, unzippable } from "./kinds";

const CodeView = lazy(() => import("./viewers/CodeView"));
const MarkdownView = lazy(() => import("./viewers/MarkdownView"));
const PdfView = lazy(() => import("./viewers/PdfView"));
const DocxView = lazy(() => import("./viewers/DocxView"));
const SheetView = lazy(() => import("./viewers/SheetView"));

export function Preview({ entry, file, index, count, full, onFull, onPrev, onNext, onClose, onDownload, onUnzip }: { entry: Entry; file: FileRef; index: number; count: number; full: boolean; onFull: (f: boolean) => void; onPrev: () => void; onNext: () => void; onClose: () => void; onDownload: () => void; onUnzip: () => void }) {
  const kind = kindOf(entry.name);
  const url = useQuery({
    queryKey: ["content-url", file.target.addr, file.spaceId, file.driveId, file.path],
    enabled: kind !== "none" && kind !== "archive",
    staleTime: Infinity,
    queryFn: () => contentUrl(file),
  });
  // Arrow keys step through the folder; Escape leaves full screen, then closes.
  useEffect(() => {
    const on = (e: KeyboardEvent) => {
      const t = e.target as HTMLElement;
      if (t.closest("input, textarea, select, [contenteditable], video, audio, [role=dialog]")) return;
      if (e.key === "ArrowLeft") onPrev();
      else if (e.key === "ArrowRight") onNext();
      else if (e.key === "Escape") full ? onFull(false) : onClose();
      else return;
      e.preventDefault();
    };
    window.addEventListener("keydown", on);
    return () => window.removeEventListener("keydown", on);
  }, [onPrev, onNext, onClose, onFull, full]);

  return (
    <aside aria-label={`Preview of ${entry.name}`} className={cn("flex min-h-0 flex-col overflow-hidden border bg-card", full ? "fixed inset-0 z-50" : "rounded-xl")}>
      <header className="flex items-center gap-2 border-b px-3 py-2">
        <div className="min-w-0 flex-1">
          <div className="truncate text-sm font-medium" title={entry.name}>
            {entry.name}
          </div>
          <div className="text-xs text-muted-foreground">
            {formatBytes(entry.sizeBytes)}
            {entry.modifiedUnixMs > 0n && ` · changed ${ago(Number(entry.modifiedUnixMs))}`}
          </div>
        </div>
        {unzippable(entry.name) && (
          <Button size="sm" variant="ghost" onClick={onUnzip} title="Unpack into a folder beside it">
            <PackageOpen /> Unzip
          </Button>
        )}
        <Button size="icon" variant="ghost" className="size-8" onClick={onDownload} title="Download" aria-label="Download">
          <Download />
        </Button>
        <Button size="icon" variant="ghost" className="size-8" onClick={() => onFull(!full)} title={full ? "Leave full screen" : "Full screen"} aria-label={full ? "Leave full screen" : "Full screen"}>
          {full ? <Minimize2 /> : <Maximize2 />}
        </Button>
        <Button size="icon" variant="ghost" className="size-8" onClick={onClose} title="Close" aria-label="Close preview">
          <X />
        </Button>
      </header>
      <div className="min-h-0 flex-1 overflow-auto">
        {kind === "archive" ? (
          <Empty icon={<FileArchive className="size-10" />} text={unzippable(entry.name) ? "An archive. Unzip it to see what is inside." : "An archive this Node cannot unpack."}>
            {unzippable(entry.name) && (
              <Button size="sm" onClick={onUnzip}>
                <PackageOpen /> Unzip into a folder
              </Button>
            )}
          </Empty>
        ) : kind === "none" ? (
          <Empty icon={<FileQuestion className="size-10" />} text="No preview for this kind of file.">
            <Button size="sm" variant="outline" onClick={onDownload}>
              <Download /> Download
            </Button>
          </Empty>
        ) : url.error ? (
          <p className="p-4 text-sm text-destructive">{String((url.error as Error).message ?? url.error)}</p>
        ) : url.data === undefined ? (
          <p className="p-4 text-sm text-muted-foreground">Opening…</p>
        ) : url.data === "" ? (
          <Empty icon={<FileQuestion className="size-10" />} text="The demo Fleet has nothing to play for this file." />
        ) : (
          <Suspense fallback={<p className="p-4 text-sm text-muted-foreground">Opening…</p>}>
            <View kind={kind} url={url.data} name={entry.name} onDownload={onDownload} />
          </Suspense>
        )}
      </div>
      {count > 1 && (
        <footer className="flex items-center justify-between border-t px-2 py-1.5 text-xs text-muted-foreground">
          <Button size="sm" variant="ghost" onClick={onPrev} aria-label="Previous file">
            <ChevronLeft /> Previous
          </Button>
          <span className="tabular-nums">
            {index + 1} of {count}
          </span>
          <Button size="sm" variant="ghost" onClick={onNext} aria-label="Next file">
            Next <ChevronRight />
          </Button>
        </footer>
      )}
    </aside>
  );
}

function View({ kind, url, name, onDownload }: { kind: ReturnType<typeof kindOf>; url: string; name: string; onDownload: () => void }) {
  const [failed, setFailed] = useState(false);
  useEffect(() => setFailed(false), [url]);
  if (failed)
    return (
      <Empty icon={<FileQuestion className="size-10" />} text={`This ${kind === "image" ? "picture" : kind} cannot play in Maître Desktop. Its format may need a codec this computer lacks.`}>
        <Button size="sm" variant="outline" onClick={onDownload}>
          <Download /> Download
        </Button>
      </Empty>
    );
  switch (kind) {
    case "image":
      return (
        <div className="flex h-full items-center justify-center bg-muted/40 p-4">
          <img src={url} alt={name} onError={() => setFailed(true)} className="max-h-full max-w-full object-contain" />
        </div>
      );
    case "video":
      return (
        <div className="flex h-full items-center justify-center bg-black">
          <video src={url} controls autoPlay={false} onError={() => setFailed(true)} className="max-h-full max-w-full" />
        </div>
      );
    case "audio":
      return (
        <div className="flex h-full items-center justify-center p-6">
          <audio src={url} controls onError={() => setFailed(true)} className="w-full max-w-md" />
        </div>
      );
    case "pdf":
      return <PdfView url={url} />;
    case "markdown":
      return <MarkdownView url={url} />;
    case "code":
      return <CodeView url={url} name={name} />;
    case "docx":
      return <DocxView url={url} />;
    case "sheet":
      return <SheetView url={url} />;
  }
  return null;
}

function Empty({ icon, text, children }: { icon: React.ReactNode; text: string; children?: React.ReactNode }) {
  return (
    <div className="flex h-full flex-col items-center justify-center gap-3 p-6 text-center text-sm text-muted-foreground">
      {icon}
      <p className="max-w-xs">{text}</p>
      {children}
    </div>
  );
}
