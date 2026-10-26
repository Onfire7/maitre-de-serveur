import { useQuery } from "@tanstack/react-query";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { services } from "@/fleet/actions";
import type { Target } from "@/transport";

export function LogsSheet({ name, target, onClose }: { name: string | null; target: Target | undefined; onClose: () => void }) {
  const q = useQuery({
    queryKey: ["logs", target?.addr, name],
    enabled: !!name && !!target,
    refetchInterval: 2000,
    queryFn: async () => (await services(target!).getServiceLogs({ name: name!, lines: 300 })).lines,
  });
  return (
    <Sheet open={!!name} onOpenChange={(o) => !o && onClose()}>
      <SheetContent className="w-[640px] sm:max-w-[640px]">
        <SheetHeader>
          <SheetTitle>{name} logs</SheetTitle>
          <SheetDescription>The last 300 lines, refreshed every two seconds.</SheetDescription>
        </SheetHeader>
        <pre className="mx-4 mb-4 min-h-0 flex-1 overflow-auto rounded-md bg-muted p-3 font-mono text-[11px] leading-relaxed">{q.error ? String(q.error) : (q.data ?? []).join("\n") || "nothing yet"}</pre>
      </SheetContent>
    </Sheet>
  );
}
