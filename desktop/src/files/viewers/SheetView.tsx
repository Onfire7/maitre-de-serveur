// Spreadsheets with SheetJS (decision 178): one tab per sheet, the first
// 1000 rows, cells as text.
import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { cn } from "@/lib/utils";
import { readBytes } from "./text";

const ROWS = 1000;

export default function SheetView({ url }: { url: string }) {
  const q = useQuery({
    queryKey: ["preview-sheet", url],
    queryFn: async () => {
      const [XLSX, bytes] = await Promise.all([import("xlsx"), readBytes(url)]);
      const wb = XLSX.read(bytes, { type: "array", cellDates: true });
      return wb.SheetNames.map((name) => {
        const rows = XLSX.utils.sheet_to_json<unknown[]>(wb.Sheets[name], { header: 1, raw: false, defval: "" });
        return { name, rows: rows.slice(0, ROWS), more: rows.length - ROWS };
      });
    },
  });
  const [tab, setTab] = useState(0);
  if (q.error) return <p className="p-4 text-sm text-destructive">This sheet could not be read: {String((q.error as Error).message)}</p>;
  if (!q.data) return <p className="p-4 text-sm text-muted-foreground">Reading…</p>;
  const sheet = q.data[Math.min(tab, q.data.length - 1)];
  const cols = Math.max(0, ...sheet.rows.map((r) => r.length));
  return (
    <div className="flex h-full flex-col">
      {q.data.length > 1 && (
        <div className="flex gap-1 border-b px-2 pt-2">
          {q.data.map((s, i) => (
            <button key={s.name} onClick={() => setTab(i)} className={cn("rounded-t-md border border-b-0 px-3 py-1 text-xs", i === tab ? "bg-card font-medium" : "text-muted-foreground hover:bg-accent")}>
              {s.name}
            </button>
          ))}
        </div>
      )}
      <div className="min-h-0 flex-1 overflow-auto">
        <table className="border-collapse text-xs">
          <tbody>
            {sheet.rows.map((r, i) => (
              <tr key={i} className={i === 0 ? "bg-muted font-medium" : undefined}>
                <td className="sticky left-0 border bg-muted px-2 py-1 text-right text-muted-foreground tabular-nums">{i + 1}</td>
                {Array.from({ length: cols }, (_, c) => (
                  <td key={c} className="max-w-64 truncate border px-2 py-1">
                    {String(r[c] ?? "")}
                  </td>
                ))}
              </tr>
            ))}
          </tbody>
        </table>
        {sheet.more > 0 && <p className="px-3 py-2 text-xs text-muted-foreground">{sheet.more} more rows. Download the file to see them.</p>}
      </div>
    </div>
  );
}
