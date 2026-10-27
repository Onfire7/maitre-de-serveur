import { isDemo } from "@/demo/flag";
import { TEXT_LIMIT } from "../kinds";

/** The first TEXT_LIMIT bytes as text, and whether there was more. */
export async function readText(url: string): Promise<{ text: string; cut: boolean }> {
  const res = await fetch(url, isDemo() ? undefined : { headers: { Range: `bytes=0-${TEXT_LIMIT - 1}` } });
  if (!res.ok) throw new Error(`${res.status} ${await res.text()}`);
  const buf = await res.arrayBuffer();
  const total = Number(res.headers.get("content-range")?.split("/")[1] ?? buf.byteLength);
  const cut = total > TEXT_LIMIT || buf.byteLength > TEXT_LIMIT;
  return { text: new TextDecoder().decode(buf.slice(0, TEXT_LIMIT)), cut };
}

export async function readBytes(url: string): Promise<ArrayBuffer> {
  const res = await fetch(url);
  if (!res.ok) throw new Error(`${res.status} ${await res.text()}`);
  return res.arrayBuffer();
}
