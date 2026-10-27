// What the file manager can preview, by file name (decision 178).

export type PreviewKind = "image" | "video" | "audio" | "pdf" | "markdown" | "code" | "docx" | "sheet" | "archive" | "none";

const BY_EXT: Record<string, PreviewKind> = {};
const add = (kind: PreviewKind, exts: string) => exts.split(" ").forEach((e) => (BY_EXT[e] = kind));
add("image", "jpg jpeg png gif webp avif bmp svg ico");
add("video", "mp4 m4v webm mov mkv ogv");
add("audio", "mp3 m4a aac flac wav ogg oga opus");
add("pdf", "pdf");
add("markdown", "md markdown");
add("docx", "docx");
add("sheet", "xlsx xlsm xls ods csv tsv");
add("archive", "zip tar tgz gz bz2 tbz2");
add(
  "code",
  "txt text log nfo srt vtt sub ass json jsonc yaml yml toml ini conf cfg env properties xml html htm css scss js mjs cjs ts tsx jsx go rs py rb php java kt swift c h cc cpp hpp cs sh bash zsh fish ps1 sql lua pl r dart vue svelte graphql proto tf dockerfile containerfile makefile nix diff patch",
);

const BY_NAME: Record<string, PreviewKind> = { dockerfile: "code", containerfile: "code", makefile: "code", readme: "code", license: "code", ".env": "code" };

export function extOf(name: string): string {
  const l = name.toLowerCase();
  if (l.endsWith(".tar.gz")) return "tgz";
  if (l.endsWith(".tar.bz2")) return "tbz2";
  const i = l.lastIndexOf(".");
  return i > 0 ? l.slice(i + 1) : "";
}

export function kindOf(name: string): PreviewKind {
  return BY_EXT[extOf(name)] ?? BY_NAME[name.toLowerCase()] ?? "none";
}

/** Archives the Node can unpack (decision 184). */
export function unzippable(name: string): boolean {
  const l = name.toLowerCase();
  return [".zip", ".tar", ".tar.gz", ".tgz", ".tar.bz2", ".tbz2"].some((e) => l.endsWith(e));
}

/** Shiki's language for a code file; "text" when it has none. */
export function languageOf(name: string): string {
  const e = extOf(name) || name.toLowerCase();
  const map: Record<string, string> = {
    yml: "yaml", jsonc: "jsonc", htm: "html", mjs: "javascript", cjs: "javascript", js: "javascript", ts: "typescript", tsx: "tsx", jsx: "jsx", py: "python", rb: "ruby", rs: "rust", kt: "kotlin", sh: "bash", bash: "bash", zsh: "bash", fish: "fish", ps1: "powershell", h: "c", cc: "cpp", hpp: "cpp", cs: "csharp", md: "markdown", conf: "ini", cfg: "ini", env: "dotenv", properties: "properties", dockerfile: "docker", containerfile: "docker", makefile: "make", tf: "hcl", srt: "text", vtt: "text", nfo: "text", txt: "text", text: "text", log: "log", sub: "text", ass: "text", patch: "diff",
  };
  return map[e] ?? e;
}

/** The first bytes a text preview reads (decision 178). */
export const TEXT_LIMIT = 1024 * 1024;
