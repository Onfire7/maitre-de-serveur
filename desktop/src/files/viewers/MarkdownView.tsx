// Markdown rendered with react-markdown and GitHub's extensions (178).
import { useQuery } from "@tanstack/react-query";
import Markdown from "react-markdown";
import remarkGfm from "remark-gfm";
import { openUrl } from "@tauri-apps/plugin-opener";
import { readText } from "./text";

export default function MarkdownView({ url }: { url: string }) {
  const q = useQuery({ queryKey: ["preview-md", url], queryFn: () => readText(url) });
  if (q.error) return <p className="p-4 text-sm text-destructive">{String((q.error as Error).message)}</p>;
  if (!q.data) return <p className="p-4 text-sm text-muted-foreground">Reading…</p>;
  return (
    <article className="space-y-3 p-5 text-sm leading-relaxed [&_a]:underline [&_blockquote]:border-l-2 [&_blockquote]:pl-3 [&_blockquote]:text-muted-foreground [&_code]:rounded [&_code]:bg-muted [&_code]:px-1 [&_h1]:text-xl [&_h1]:font-semibold [&_h2]:text-lg [&_h2]:font-semibold [&_h3]:font-semibold [&_hr]:border-border [&_li]:ml-5 [&_ol]:list-decimal [&_pre]:overflow-x-auto [&_pre]:rounded-md [&_pre]:bg-muted [&_pre]:p-3 [&_pre_code]:bg-transparent [&_pre_code]:p-0 [&_table]:w-full [&_table]:border-collapse [&_td]:border [&_td]:px-2 [&_td]:py-1 [&_th]:border [&_th]:bg-muted [&_th]:px-2 [&_th]:py-1 [&_th]:text-left [&_ul]:list-disc">
      <Markdown
        remarkPlugins={[remarkGfm]}
        components={{
          // Links open in the system browser; the Console never navigates away.
          a: ({ href, children }) => (
            <a
              href={href}
              onClick={(e) => {
                e.preventDefault();
                if (href && /^https?:/.test(href)) openUrl(href).catch(() => {});
              }}
            >
              {children}
            </a>
          ),
          img: ({ alt }) => <span className="text-muted-foreground">[{alt || "image"}]</span>,
        }}
      >
        {q.data.text}
      </Markdown>
      {q.data.cut && <p className="text-xs text-muted-foreground">Showing the first 1 MB.</p>}
    </article>
  );
}
