// Text and code with Shiki's highlighting (decision 178), first 1 MB.
import { useQuery } from "@tanstack/react-query";
import { useTheme } from "@/app/theme";
import { languageOf } from "../kinds";
import { readText } from "./text";

export default function CodeView({ url, name }: { url: string; name: string }) {
  const { theme } = useTheme();
  const q = useQuery({
    queryKey: ["preview-code", url, theme],
    queryFn: async () => {
      const { text, cut } = await readText(url);
      const { codeToHtml, bundledLanguages } = await import("shiki");
      const lang = languageOf(name);
      const html = await codeToHtml(text, { lang: lang in bundledLanguages ? lang : "text", theme: theme === "dark" ? "github-dark-default" : "github-light-default" });
      return { html, cut };
    },
  });
  if (q.error) return <p className="p-4 text-sm text-destructive">{String((q.error as Error).message)}</p>;
  if (!q.data) return <p className="p-4 text-sm text-muted-foreground">Reading…</p>;
  return (
    <div className="text-xs">
      {/* Shiki escapes the file's text; the markup is its own. */}
      <div className="[&_pre]:min-h-full [&_pre]:overflow-x-auto [&_pre]:p-4 [&_pre]:!bg-transparent" dangerouslySetInnerHTML={{ __html: q.data.html }} />
      {q.data.cut && <p className="border-t px-4 py-2 text-muted-foreground">Showing the first 1 MB. Download the file to see all of it.</p>}
    </div>
  );
}
