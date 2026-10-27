// PDFs with pdf.js (decision 178): the web view on Linux has no viewer of
// its own. Pages render as they scroll into view.
import { useEffect, useRef, useState } from "react";
import * as pdfjs from "pdfjs-dist";
import workerUrl from "pdfjs-dist/build/pdf.worker.min.mjs?url";
import type { PDFDocumentProxy } from "pdfjs-dist";

pdfjs.GlobalWorkerOptions.workerSrc = workerUrl;

export default function PdfView({ url }: { url: string }) {
  const [doc, setDoc] = useState<PDFDocumentProxy>();
  const [error, setError] = useState("");
  const box = useRef<HTMLDivElement>(null);
  const [width, setWidth] = useState(600);
  useEffect(() => {
    let gone = false;
    const task = pdfjs.getDocument({ url });
    task.promise.then((d) => !gone && setDoc(d)).catch((e) => !gone && setError(String(e?.message ?? e)));
    return () => {
      gone = true;
      task.destroy();
    };
  }, [url]);
  useEffect(() => {
    const el = box.current;
    if (!el) return;
    const ro = new ResizeObserver(() => setWidth(Math.max(200, el.clientWidth - 32)));
    ro.observe(el);
    return () => ro.disconnect();
  }, []);
  return (
    <div ref={box} className="space-y-4 bg-muted/40 p-4">
      {error && <p className="text-sm text-destructive">{error}</p>}
      {!doc && !error && <p className="text-sm text-muted-foreground">Reading…</p>}
      {doc && Array.from({ length: doc.numPages }, (_, i) => <Page key={i} doc={doc} n={i + 1} width={width} />)}
    </div>
  );
}

function Page({ doc, n, width }: { doc: PDFDocumentProxy; n: number; width: number }) {
  const canvas = useRef<HTMLCanvasElement>(null);
  const [seen, setSeen] = useState(n <= 2);
  const [ratio, setRatio] = useState(1.294);
  useEffect(() => {
    const el = canvas.current;
    if (!el || seen) return;
    const io = new IntersectionObserver(([e]) => e.isIntersecting && setSeen(true), { rootMargin: "400px" });
    io.observe(el);
    return () => io.disconnect();
  }, [seen]);
  useEffect(() => {
    if (!seen) return;
    let task: { cancel: () => void } | undefined;
    doc.getPage(n).then((page) => {
      const base = page.getViewport({ scale: 1 });
      setRatio(base.height / base.width);
      const scale = (width / base.width) * (window.devicePixelRatio || 1);
      const vp = page.getViewport({ scale });
      const c = canvas.current;
      if (!c) return;
      c.width = vp.width;
      c.height = vp.height;
      task = page.render({ canvas: c, viewport: vp });
    });
    return () => task?.cancel();
  }, [seen, doc, n, width]);
  return <canvas ref={canvas} className="mx-auto block bg-white shadow" style={{ width, height: width * ratio }} aria-label={`Page ${n}`} />;
}
