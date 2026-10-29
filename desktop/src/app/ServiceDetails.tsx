// A Service's every setting, shown when its row on the Services tab opens
// (decision 192), with a contents list that stays in view beside it.
// Settings and auto update (193), Access and Storage (194, shown as their
// editors) and the image (195) change here, in place; ports and root
// powers through the flyout's sheets. Its usage over time shows too (210).
// A Service of several containers lists them, each with its state, image,
// logs and container definition (plan C1).
import { useEffect, useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { ArrowUpCircle, Copy, Eye, EyeOff, ScrollText, Truck, Trash2, Undo2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { cn } from "@/lib/utils";
import { ago, formatUptime } from "@/lib/format";
import type { FleetService, FleetSnapshot } from "@/fleet/useFleet";
import { declined, grantsOf, optionNote } from "@/fleet/grants";
import { setImage, setSettings, targetFor } from "@/fleet/actions";
import { canRollBack, componentsOf, componentStatus, isMulti, logSource } from "@/fleet/components";
import { useNodeLogs } from "./LogsTab";
import { catalogById } from "./status";
import type { FlyoutItem } from "./ServiceFlyout";
import { AccessEditor, StorageEditor } from "./ServiceSheets";
import { ServiceUsage } from "./Usage";
import type { ServiceEdit } from "./routes";

const STATE = ["Unknown", "Stopped", "Starting", "Running", "Failed", "Removing"];

/** Values of settings named like these are hidden until asked for. */
const SECRET = /pass|secret|token|key|credential/i;

export function ServiceDetails({ service: s, snap, onAct, now, edit }: { service: FleetService; snap: FleetSnapshot | undefined; onAct: (it: FlyoutItem) => void; now: number; edit?: ServiceEdit }) {
  const spec = s.spec;
  const st = s.status;
  const entry = catalogById.get(spec.catalogId);
  const grants = grantsOf(snap?.records, spec.name).filter((g) => !g.grant.nodeId || g.grant.nodeId === spec.nodeId);
  const running = st?.state === 3 || st?.state === 2;
  const settingLabel = (k: string) => entry?.settings?.find((x) => x.key === k)?.label;
  const [shown, setShown] = useState<Set<string>>(new Set());
  // The Catalog's settings in its order, then any others the Service has.
  const settingKeys = [...new Set([...(entry?.settings ?? []).map((x) => x.key), ...Object.keys(spec.settings).sort((a, b) => a.localeCompare(b))])];
  const qc = useQueryClient();
  const [draft, setDraft] = useState<Record<string, string> | null>(null);
  const [saving, setSaving] = useState(false);
  const [saveError, setSaveError] = useState("");
  useEffect(() => setDraft(null), [spec.name]);
  const target = targetFor(snap, [], spec.nodeId);
  const multi = isMulti(spec);
  const parts = componentsOf(spec);
  const openLogs = useNodeLogs();
  // The image being typed, while Change image is open (195), and in a
  // Service of several containers the component it is for.
  const [newImage, setNewImage] = useState<string | null>(null);
  const [imageFor, setImageFor] = useState("");
  const [imageError, setImageError] = useState("");
  useEffect(() => setNewImage(null), [spec.name]);
  const changing = parts.find((c) => c.name === imageFor) ?? parts[0];
  const startChange = (component: string) => {
    setImageError("");
    setImageFor(component);
    setNewImage((parts.find((c) => c.name === component) ?? parts[0]).image.split("@")[0]);
  };
  async function changeImage(image: string) {
    if (!target) return toast.error(`No address for ${s.node?.name ?? "its Node"}`);
    setSaving(true);
    setImageError("");
    try {
      await setImage(target, spec.name, image, s.node?.name, multi ? imageFor : "");
      toast.success(`${multi ? `${spec.name}: ${imageFor}` : spec.name} now runs ${image}${running ? ", restarting" : ""}`);
      setNewImage(null);
      qc.invalidateQueries({ queryKey: ["fleet"] });
    } catch (e) {
      if (!declined(e)) setImageError(String((e as Error).message ?? e));
    } finally {
      setSaving(false);
    }
  }

  async function save(settings: Record<string, string>, autoUpdate?: boolean) {
    const t = target;
    if (!t) return toast.error(`No address for ${s.node?.name ?? "its Node"}`);
    setSaving(true);
    setSaveError("");
    try {
      await setSettings(t, spec.name, settings, autoUpdate, s.node?.name);
      toast.success(autoUpdate === undefined ? `${spec.name}: settings saved${running ? ", restarting" : ""}` : `${spec.name} ${autoUpdate ? "updates by itself" : "updates only when you choose"}`);
      setDraft(null);
      qc.invalidateQueries({ queryKey: ["fleet"] });
    } catch (e) {
      if (declined(e)) return;
      const msg = String((e as Error).message ?? e);
      if (autoUpdate === undefined) setSaveError(msg);
      else toast.error(msg);
    } finally {
      setSaving(false);
    }
  }

  const sections: { id: string; title: string; edit?: [FlyoutItem, string]; button?: React.ReactNode; body: React.ReactNode }[] = [
    {
      id: "status",
      title: "Status",
      body: (
        <Rows
          rows={[
            ["State", `${STATE[st?.state ?? 0]}${st?.state === 3 ? (st.healthy ? ", healthy" : ", not answering its health check") : ""}`],
            ["Message", st?.message],
            ["Runs on", s.node?.name ?? "Node unknown"],
            ["Up for", running && st?.startedUnixMs ? formatUptime(Math.max(0, Math.floor((now - Number(st.startedUnixMs)) / 1000))) : undefined],
            ["Restarts", st?.restarts ? String(st.restarts) : "None"],
            ["Installed", spec.createdUnixMs ? `${new Date(Number(spec.createdUnixMs)).toLocaleDateString()} (${ago(Number(spec.createdUnixMs), now)})` : undefined],
            ["From the Catalog", entry ? `${entry.name}: ${entry.summary}` : spec.catalogId ? `${spec.catalogId} (no longer in the Catalog)` : "No, a custom Service"],
            ["Network infrastructure", spec.infrastructure ? "Yes: it starts before the network is up, and its Node keeps a fixed address" : undefined],
          ]}
        />
      ),
    },
    ...(multi
      ? [
          {
            id: "components",
            title: "Containers",
            body: (
              <Table
                head={["Container", "State", "Image", ""]}
                rows={parts.map((c) => {
                  const cs = componentStatus(st, c.name);
                  const role = [c.name === spec.webComponent && "web page", spec.components.find((x) => x.name === c.name)?.waitHealthy && "others wait for it"].filter(Boolean).join(", ");
                  return [
                    <span key="n">
                      <span className="font-medium">{c.name}</span>
                      {role && <span className="block text-xs text-muted-foreground">{role}</span>}
                    </span>,
                    <span key="s">
                      {cs ? `${STATE[cs.state]}${cs.state === 3 && cs.healthy ? ", healthy" : ""}` : <span className="text-muted-foreground">not reported</span>}
                      {cs?.restarts ? <span className="block text-xs text-muted-foreground">{cs.restarts} restarts</span> : null}
                      {cs?.message && <span className="block text-xs text-muted-foreground">{cs.message}</span>}
                    </span>,
                    <Image key="i" image={c.image} />,
                    <Button key="l" size="sm" variant="ghost" className="h-7" disabled={s.nodeUnreachable} onClick={() => openLogs(spec.nodeId, logSource(spec.name, c.name))} aria-label={`Logs of ${c.name}`}>
                      <ScrollText /> Logs
                    </Button>,
                  ];
                })}
              />
            ),
          },
        ]
      : []),
    {
      id: "usage",
      title: "Usage",
      body: <ServiceUsage service={s} snap={snap} />,
    },
    {
      id: "web",
      title: "Web page",
      body: spec.webPort ? (
        <Rows
          rows={[
            ["At home", st?.url ? <Link url={st.url} /> : "Not yet"],
            ["From anywhere", st?.remoteUrl ? <Link url={st.remoteUrl} /> : undefined],
            ["Tailscale", st?.remoteState || undefined],
            ["Container port", `${spec.webPort}${spec.webPath && spec.webPath !== "/" ? `, at ${spec.webPath}` : ""}`],
            ["Health check", spec.healthHttp || spec.webPath || "/"],
            ["Port on the Node", spec.webHostPort ? `${spec.webHostPort} (loopback only, for Caddy)` : undefined],
          ]}
        />
      ) : (
        <p className="text-muted-foreground">It has no web page.</p>
      ),
    },
    {
      id: "access",
      title: "Access",
      body: <AccessEditor service={s} target={target} snap={snap} />,
    },
    {
      id: "ports",
      title: "Ports",
      edit: entry?.port_forward ? ["ports", "Port forwarding"] : undefined,
      body: spec.hostPorts.length ? (
        <Rows
          rows={[
            ["On the Node", spec.hostPorts.join(", ")],
            ["Router forwards them", spec.forwardPorts ? "Yes, from the internet" : "No"],
            ["Router says", st?.forwardState || undefined],
          ]}
        />
      ) : (
        <p className="text-muted-foreground">It uses no ports of the Node's own; only its web page, through Caddy.</p>
      ),
    },
    {
      id: "storage",
      title: "Storage",
      body: <StorageEditor service={s} target={target} snap={snap} />,
    },
    {
      id: "settings",
      title: "Settings",
      button:
        settingKeys.length > 0 && !draft ? (
          <Button size="sm" variant="ghost" className="ml-auto h-7" disabled={s.nodeUnreachable} onClick={() => (setSaveError(""), setDraft(Object.fromEntries(settingKeys.map((k) => [k, spec.settings[k] ?? entry?.settings?.find((x) => x.key === k)?.default ?? ""]))))}>
            Change
          </Button>
        ) : undefined,
      body: draft ? (
        <form
          className="space-y-3"
          onSubmit={(e) => {
            e.preventDefault();
            save(draft);
          }}
        >
          <div className="divide-y rounded-md border">
            {settingKeys.map((k) => {
              const type = entry?.settings?.find((x) => x.key === k)?.type ?? "text";
              return (
                <label key={k} className="flex items-center gap-3 px-3 py-2">
                  <span className="min-w-0 flex-1">
                    {settingLabel(k) ?? k}
                    {settingLabel(k) && <code className="ml-1.5 text-xs text-muted-foreground">{k}</code>}
                  </span>
                  {type === "bool" ? (
                    <Switch checked={draft[k] === "true"} onCheckedChange={(v) => setDraft({ ...draft, [k]: v ? "true" : "false" })} aria-label={settingLabel(k) ?? k} />
                  ) : (
                    <Input className="h-8 w-64" type={SECRET.test(k) ? "password" : "text"} value={draft[k]} placeholder={type === "timezone" ? "auto: the Node's time zone" : undefined} onChange={(e) => setDraft({ ...draft, [k]: e.target.value })} aria-label={settingLabel(k) ?? k} />
                  )}
                </label>
              );
            })}
          </div>
          {saveError && <p className="rounded-md border border-destructive/60 bg-destructive/5 p-2 text-sm">{saveError}</p>}
          <div className="flex items-center gap-2">
            <Button type="submit" size="sm" disabled={saving}>
              {running ? "Save and restart" : "Save"}
            </Button>
            <Button type="button" size="sm" variant="ghost" onClick={() => setDraft(null)}>
              Cancel
            </Button>
            {running && <span className="text-xs text-muted-foreground">{spec.name} restarts to use them.</span>}
          </div>
        </form>
      ) : settingKeys.length ? (
        <Table
          head={["Setting", "Value"]}
          rows={settingKeys.map((k) => {
            const v = spec.settings[k];
            const hide = SECRET.test(k) && !shown.has(k);
            return [
              <span key="k">
                {settingLabel(k) ?? k}
                {settingLabel(k) && <code className="ml-1.5 text-xs text-muted-foreground">{k}</code>}
              </span>,
              <span key="v" className="flex items-center gap-1">
                <code className="break-all">{v === undefined ? <span className="text-muted-foreground">not set</span> : v === "" ? <span className="text-muted-foreground">(empty)</span> : hide ? "••••••••" : v}</code>
                {SECRET.test(k) && (
                  <button className="rounded p-0.5 text-muted-foreground hover:text-foreground" aria-label={hide ? `Show ${k}` : `Hide ${k}`} onClick={() => setShown((p) => (p.has(k) ? new Set([...p].filter((x) => x !== k)) : new Set([...p, k])))}>
                    {hide ? <Eye className="size-3.5" /> : <EyeOff className="size-3.5" />}
                  </button>
                )}
              </span>,
            ];
          })}
        />
      ) : (
        <p className="text-muted-foreground">It has no settings.</p>
      ),
    },
    {
      id: "image",
      title: "Image and updates",
      button:
        newImage === null && !multi ? (
          <Button size="sm" variant="ghost" className="ml-auto h-7" disabled={s.nodeUnreachable} onClick={() => startChange("")}>
            Change image
          </Button>
        ) : undefined,
      body: (
        <div className="space-y-3">
          {newImage !== null && (
            <form
              className="space-y-2 rounded-md border p-3"
              onSubmit={(e) => {
                e.preventDefault();
                changeImage(newImage.trim());
              }}
            >
              <label className="block space-y-1.5">
                <span className="text-muted-foreground">{multi ? `Image for ${imageFor} to run` : "Image to run"}</span>
                <Input className="font-mono text-xs" value={newImage} onChange={(e) => setNewImage(e.target.value)} placeholder="docker.io/jellyfin/jellyfin:10.11" aria-label="Image to run" autoFocus />
              </label>
              <p className="text-xs text-muted-foreground">A tag is pinned to the image it names now; it moves only when updated. The current image stays for Roll back. An image from another repository may need its root powers allowed again.</p>
              {imageError && <p className="rounded-md border border-destructive/60 bg-destructive/5 p-2 text-sm">{imageError}</p>}
              <div className="flex items-center gap-2">
                <Button type="submit" size="sm" disabled={saving || !newImage.trim() || newImage.trim() === changing.image}>
                  {saving ? "Pulling…" : running ? "Change and restart" : "Change"}
                </Button>
                <Button type="button" size="sm" variant="ghost" onClick={() => setNewImage(null)}>
                  Cancel
                </Button>
              </div>
            </form>
          )}
          {multi && (
            <Table
              head={["Container", "Image", ""]}
              rows={parts.map((c) => [
                <span key="n" className="font-medium">
                  {c.name}
                </span>,
                <span key="i" className="block space-y-0.5">
                  <Image image={c.image} />
                  {c.previousImage && (
                    <span className="block text-xs text-muted-foreground">
                      Before the last update: <Image image={c.previousImage} />
                    </span>
                  )}
                </span>,
                <Button key="c" size="sm" variant="ghost" className="h-7" disabled={s.nodeUnreachable || newImage !== null} onClick={() => startChange(c.name)} aria-label={`Change the image of ${c.name}`}>
                  Change
                </Button>,
              ])}
            />
          )}
          <Rows
            rows={[
              ["Image", multi ? undefined : <Image key="i" image={spec.image} />],
              ["Before the last update", !multi && spec.previousImage ? <Image key="p" image={spec.previousImage} /> : undefined],
              [
                "Updates by itself",
                <label key="a" className="flex items-center gap-2">
                  <Switch checked={spec.autoUpdate} disabled={saving || s.nodeUnreachable} onCheckedChange={(v) => save({ ...spec.settings }, v)} aria-label="Updates by itself" />
                  {spec.autoUpdate ? "Yes, daily" : "No, only when you choose Update"}
                </label>,
              ],
            ]}
          />
          <div className="flex gap-2">
            <Button size="sm" variant="outline" disabled={s.nodeUnreachable} onClick={() => onAct("update")}>
              <ArrowUpCircle /> Update now
            </Button>
            {canRollBack(spec) && (
              <Button size="sm" variant="outline" disabled={s.nodeUnreachable} onClick={() => onAct("rollback")}>
                <Undo2 /> Roll back
              </Button>
            )}
          </div>
        </div>
      ),
    },
    {
      id: "powers",
      title: "Root powers",
      edit: grants.length ? ["grants", "Review"] : undefined,
      body: grants.length ? (
        <ul className="space-y-2">
          {grants.map(({ record, grant: g }) => (
            <li key={record.id}>
              {g.component && <span className="mr-1.5 font-medium">{g.component}:</span>}
              <code>{g.option}</code>
              <div className="text-xs text-muted-foreground">
                {optionNote(g.option)} Allowed {g.deviceName ? `from ${g.deviceName} ` : ""}
                {g.grantedUnixMs ? ago(Number(g.grantedUnixMs), now) : ""}.
              </div>
            </li>
          ))}
        </ul>
      ) : (
        <p className="text-muted-foreground">None. It runs inside the limits Maître puts on every container.</p>
      ),
    },
    {
      id: "container",
      title: "Container definition",
      body: (
        <div className="space-y-3">
          {parts.map((c) => (
            <div key={c.name}>
              {multi && <h4 className="mb-1 font-medium">{c.name}</h4>}
              <div className="relative">
                <Button size="icon" variant="ghost" className="absolute top-1 right-1 size-7" aria-label={multi ? `Copy the container definition of ${c.name}` : "Copy the container definition"} onClick={() => navigator.clipboard?.writeText(c.quadlet).then(() => toast("Copied"), () => toast.error("Could not copy"))}>
                  <Copy />
                </Button>
                <pre className="max-h-72 overflow-auto rounded-md border bg-muted/40 p-3 text-xs leading-relaxed">{c.quadlet.trim() || "(empty)"}</pre>
              </div>
            </div>
          ))}
          <p className="text-xs text-muted-foreground">{multi ? "The Quadlet templates the Node fills in for its folders and ports. The Node adds each container's network, start order and health wait." : "The Quadlet template the Node fills in for its folders and ports."}</p>
        </div>
      ),
    },
    {
      id: "manage",
      title: "Move or remove",
      body: (
        <div className="flex flex-wrap gap-2">
          <Button size="sm" variant="outline" disabled={s.nodeUnreachable} onClick={() => onAct("move")}>
            <Truck /> Move to another Node…
          </Button>
          <Button size="sm" variant="outline" className="text-destructive hover:text-destructive" onClick={() => onAct("remove")}>
            <Trash2 /> Remove…
          </Button>
        </div>
      ),
    },
  ];

  // The contents list follows the section nearest the top of the view.
  const refs = useRef(new Map<string, HTMLElement>());
  const [active, setActive] = useState("status");
  // A section picked from the list stays marked while the view scrolls to
  // it, even one too near the end to reach the top.
  const picked = useRef(0);
  useEffect(() => {
    const seen = new Map<string, boolean>();
    const io = new IntersectionObserver(
      (es) => {
        for (const e of es) seen.set((e.target as HTMLElement).dataset.section!, e.isIntersecting);
        const first = sections.find((x) => seen.get(x.id));
        if (first && Date.now() > picked.current) setActive(first.id);
      },
      { rootMargin: "0px 0px -60% 0px" },
    );
    refs.current.forEach((el) => io.observe(el));
    return () => io.disconnect();
  }, [spec.name]);
  // Opened to change a section: bring it into view.
  useEffect(() => {
    if (!edit) return;
    setActive(edit);
    picked.current = Date.now() + 1000;
    requestAnimationFrame(() => refs.current.get(edit)?.scrollIntoView({ behavior: "smooth", block: "start" }));
  }, [edit, spec.name]);

  return (
    <div className="flex gap-6 border-t bg-muted/20 px-4 py-4">
      <nav aria-label={`${spec.name} settings`} className="sticky top-0 hidden w-44 shrink-0 self-start md:block">
        <ul className="space-y-0.5 border-l text-sm">
          {sections.map((x) => (
            <li key={x.id}>
              <button
                onClick={() => {
                  setActive(x.id);
                  picked.current = Date.now() + 1000;
                  refs.current.get(x.id)?.scrollIntoView({ behavior: "smooth", block: "start" });
                }}
                className={cn("-ml-px block w-full border-l-2 px-3 py-1 text-left", active === x.id ? "border-primary font-medium text-foreground" : "border-transparent text-muted-foreground hover:text-foreground")}
              >
                {x.title}
              </button>
            </li>
          ))}
        </ul>
      </nav>
      <div className="min-w-0 flex-1 space-y-6">
        {sections.map((x) => (
          <section
            key={x.id}
            data-section={x.id}
            ref={(el) => {
              if (el) refs.current.set(x.id, el);
              else refs.current.delete(x.id);
            }}
            className="scroll-mt-2 text-sm"
          >
            <header className="mb-2 flex items-center gap-2">
              <h3 className="font-semibold">{x.title}</h3>
              {x.button}
              {x.edit && (
                <Button size="sm" variant="ghost" className="ml-auto h-7" onClick={() => onAct(x.edit![0])}>
                  {x.edit[1]}
                </Button>
              )}
            </header>
            {x.body}
          </section>
        ))}
      </div>
    </div>
  );
}

function Rows({ rows }: { rows: [string, React.ReactNode | undefined][] }) {
  return (
    <dl className="grid grid-cols-[11rem_1fr] gap-x-4 gap-y-1">
      {rows
        .filter(([, v]) => v !== undefined && v !== "")
        .map(([k, v], i) => (
          <div key={k || i} className="contents">
            <dt className="text-muted-foreground">{k}</dt>
            <dd className="min-w-0 break-words">{v}</dd>
          </div>
        ))}
    </dl>
  );
}

function Table({ head, rows }: { head: string[]; rows: React.ReactNode[][] }) {
  return (
    <div className="overflow-x-auto rounded-md border">
      <table className="w-full text-left text-sm">
        <thead className="text-xs text-muted-foreground">
          <tr className="border-b">
            {head.map((h) => (
              <th key={h} className="px-3 py-1.5 font-normal">
                {h}
              </th>
            ))}
          </tr>
        </thead>
        <tbody className="divide-y">
          {rows.map((r, i) => (
            <tr key={i}>
              {r.map((c, j) => (
                <td key={j} className="px-3 py-1.5 align-top">
                  {c}
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function Link({ url }: { url: string }) {
  return (
    <span className="break-all">
      {url}
      <button className="ml-1 inline-flex rounded p-0.5 align-middle text-muted-foreground hover:text-foreground" aria-label={`Copy ${url}`} onClick={() => navigator.clipboard?.writeText(url).then(() => toast(`Copied ${url}`), () => toast.error("Could not copy"))}>
        <Copy className="size-3.5" />
      </button>
    </span>
  );
}

/** An image reference with its digest shortened. */
function Image({ image }: { image: string }) {
  const [ref, digest] = image.split("@");
  return (
    <code className="break-all" title={image}>
      {ref}
      {digest && <span className="text-muted-foreground">@{digest.replace(/^sha256:/, "").slice(0, 12)}</span>}
    </code>
  );
}
