// File contents for the demo Fleet's previews: what was uploaded in the
// browser, else a made-up file of the right kind (a document, a sheet, a
// picture, a chime). Videos have none: the demo says so.
import { demoFleet } from "./fleet";
import { extOf, kindOf } from "@/files/kinds";
import type { FileRef } from "@/files/content";

const made = new Map<string, Promise<string>>();

export function demoBlobKey(f: { nodeId: string; spaceId?: string; driveId?: string; path: string }) {
  return f.spaceId ? `space:${f.spaceId}:${f.path}` : `drive:${f.driveId}@${f.nodeId}:${f.path}`;
}

export async function demoContentUrl(f: FileRef): Promise<string> {
  const key = demoBlobKey(f);
  const up = demoFleet().blobs.get(key);
  if (up) return URL.createObjectURL(up);
  let p = made.get(key);
  if (!p) {
    p = sample(f.path.split("/").pop() ?? f.path).then((b) => (b ? URL.createObjectURL(b) : ""));
    made.set(key, p);
  }
  return p;
}

async function sample(name: string): Promise<Blob | undefined> {
  const kind = kindOf(name);
  const ext = extOf(name);
  switch (kind) {
    case "markdown":
      return new Blob([`# ${name.replace(/\.\w+$/, "")}\n\nThis Fleet keeps **media**, *documents* and backups.\n\n| Space | Drives | Use |\n| --- | --- | --- |\n| Media | 3 | films and shows |\n| General | 2 | Service data |\n\n- [x] Add a second Node\n- [ ] Turn on remote access\n\n\`\`\`yaml\nspaces:\n  - media\n  - general\n\`\`\`\n`], { type: "text/markdown" });
    case "code":
      if (ext === "yaml" || ext === "yml") return new Blob([`# ${name}\nserver:\n  name: alder\n  port: 8096\nlibraries:\n  - name: Movies\n    path: /media/movies\n  - name: Shows\n    path: /media/shows\n`], { type: "text/yaml" });
      if (ext === "json") return new Blob([JSON.stringify({ name, spaces: ["media", "general"], nodes: { alder: "10.0.0.5", basalt: "10.0.0.6" } }, null, 2)], { type: "application/json" });
      return new Blob([`${name}\n\nNotes from the demo Fleet.\n\n- Alder holds Apollo, Aspen and Atlas.\n- Basalt holds Basin and Birch.\n- Media spans both Nodes.\n`], { type: "text/plain" });
    case "image":
      return new Blob([poster(name)], { type: "image/svg+xml" });
    case "pdf":
      return new Blob([pdf(name)], { type: "application/pdf" });
    case "audio":
      return chime();
    case "sheet": {
      const XLSX = await import("xlsx");
      const rows = [["Month", "Power (kWh)", "Drives", "Cost"], ["July", 41.2, 5, 9.8], ["August", 44.9, 5, 10.7], ["September", 47.3, 6, 11.2], ["October", 46.1, 6, 11.0]];
      const wb = XLSX.utils.book_new();
      XLSX.utils.book_append_sheet(wb, XLSX.utils.aoa_to_sheet(rows), "Running costs");
      XLSX.utils.book_append_sheet(wb, XLSX.utils.aoa_to_sheet([["Drive", "Node", "Size (TB)"], ["Apollo", "Alder", 1], ["Aspen", "Alder", 4.4], ["Basin", "Basalt", 8.8]]), "Drives");
      if (ext === "csv") return new Blob([XLSX.utils.sheet_to_csv(wb.Sheets["Running costs"])], { type: "text/csv" });
      return new Blob([XLSX.write(wb, { type: "array", bookType: "xlsx" })], { type: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet" });
    }
    case "docx":
      return docx(name);
  }
  return undefined;
}

function poster(name: string) {
  return `<svg xmlns="http://www.w3.org/2000/svg" width="1200" height="800" viewBox="0 0 1200 800"><defs><linearGradient id="g" x1="0" y1="0" x2="1" y2="1"><stop offset="0" stop-color="#1e3a8a"/><stop offset="1" stop-color="#f59e0b"/></linearGradient></defs><rect width="1200" height="800" fill="url(#g)"/><circle cx="900" cy="250" r="120" fill="#fde68a" opacity=".85"/><path d="M0 640 L300 420 L520 600 L760 380 L1200 700 L1200 800 L0 800Z" fill="#0f172a" opacity=".8"/><text x="60" y="110" font-family="sans-serif" font-size="56" fill="white">${name.replace(/[<&>]/g, "")}</text></svg>`;
}

/** A one-page PDF with a line of text; offsets computed so readers accept it. */
function pdf(name: string) {
  const text = `Maître demo: ${name}`.replace(/[()\\]/g, "").replace(/[^\x20-\x7e]/g, "e");
  const stream = `BT /F1 28 Tf 72 720 Td (${text}) Tj 0 -40 Td /F1 14 Tf (A PDF made up by the demo Fleet for the file manager preview.) Tj ET`;
  const objs = [
    "<< /Type /Catalog /Pages 2 0 R >>",
    "<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
    "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>",
    `<< /Length ${stream.length} >>\nstream\n${stream}\nendstream`,
    "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
  ];
  let out = "%PDF-1.4\n";
  const offsets: number[] = [];
  objs.forEach((o, i) => {
    offsets.push(out.length);
    out += `${i + 1} 0 obj\n${o}\nendobj\n`;
  });
  const xref = out.length;
  out += `xref\n0 ${objs.length + 1}\n0000000000 65535 f \n${offsets.map((o) => `${String(o).padStart(10, "0")} 00000 n \n`).join("")}trailer\n<< /Size ${objs.length + 1} /Root 1 0 R >>\nstartxref\n${xref}\n%%EOF\n`;
  return out;
}

/** Two seconds of a soft two-note chime as a WAV. */
function chime() {
  const rate = 22050;
  const n = rate * 2;
  const buf = new ArrayBuffer(44 + n * 2);
  const v = new DataView(buf);
  const str = (o: number, s: string) => [...s].forEach((c, i) => v.setUint8(o + i, c.charCodeAt(0)));
  str(0, "RIFF");
  v.setUint32(4, 36 + n * 2, true);
  str(8, "WAVEfmt ");
  v.setUint32(16, 16, true);
  v.setUint16(20, 1, true);
  v.setUint16(22, 1, true);
  v.setUint32(24, rate, true);
  v.setUint32(28, rate * 2, true);
  v.setUint16(32, 2, true);
  v.setUint16(34, 16, true);
  str(36, "data");
  v.setUint32(40, n * 2, true);
  for (let i = 0; i < n; i++) {
    const t = i / rate;
    const f = t < 1 ? 660 : 880;
    const env = Math.exp(-3 * (t % 1));
    v.setInt16(44 + i * 2, Math.sin(2 * Math.PI * f * t) * env * 12000, true);
  }
  return new Blob([buf], { type: "audio/wav" });
}

async function docx(name: string) {
  const { default: JSZip } = await import("jszip");
  const z = new JSZip();
  z.file("[Content_Types].xml", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/></Types>`);
  z.file("_rels/.rels", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/></Relationships>`);
  const p = (t: string, bold = false, size = 24) => `<w:p><w:r><w:rPr>${bold ? "<w:b/>" : ""}<w:sz w:val="${size}"/></w:rPr><w:t xml:space="preserve">${t}</w:t></w:r></w:p>`;
  z.file(
    "word/document.xml",
    `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>${p(name.replace(/\.docx$/i, "").replace(/[<&>]/g, ""), true, 40)}${p("Dear neighbour,")}${p("The home server in the hall closet now keeps everyone's photos on two machines, so a failed drive no longer loses a summer of pictures.")}${p("Ask for an invitation from any of the family's Trusted Devices.")}${p("Kind regards,")}</w:body></w:document>`,
  );
  return z.generateAsync({ type: "blob", mimeType: "application/vnd.openxmlformats-officedocument.wordprocessingml.document" });
}
