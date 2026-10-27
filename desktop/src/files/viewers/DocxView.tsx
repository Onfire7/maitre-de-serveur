// Word documents with docx-preview (decision 178), rendered as pages.
import { useEffect, useRef, useState } from "react";
import { readBytes } from "./text";

export default function DocxView({ url }: { url: string }) {
  const box = useRef<HTMLDivElement>(null);
  const [state, setState] = useState<"reading" | "done" | string>("reading");
  useEffect(() => {
    let gone = false;
    (async () => {
      try {
        const [{ renderAsync }, bytes] = await Promise.all([import("docx-preview"), readBytes(url)]);
        if (gone || !box.current) return;
        box.current.innerHTML = "";
        await renderAsync(bytes, box.current, undefined, { inWrapper: true, ignoreLastRenderedPageBreak: true, breakPages: true, experimental: false });
        if (!gone) setState("done");
      } catch (e) {
        if (!gone) setState(String((e as Error).message ?? e));
      }
    })();
    return () => {
      gone = true;
    };
  }, [url]);
  return (
    <div className="bg-muted/40">
      {state === "reading" && <p className="p-4 text-sm text-muted-foreground">Reading…</p>}
      {state !== "reading" && state !== "done" && <p className="p-4 text-sm text-destructive">This document could not be shown: {state}</p>}
      <div ref={box} className="[&_.docx-wrapper]:!bg-transparent [&_.docx-wrapper]:!p-4 [&_section.docx]:!mb-4 [&_section.docx]:shadow" />
    </div>
  );
}
