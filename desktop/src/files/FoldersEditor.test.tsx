// @vitest-environment jsdom
import "@/test/dom";
import { describe, expect, it } from "vitest";
import { useState } from "react";
import { render, screen, fireEvent, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { create } from "@bufbuild/protobuf";
import { SpaceSchema } from "@/gen/maitre/v1/storage_pb";
import { FoldersEditor, defaultFolders, type Folders } from "./FoldersEditor";
import type { FolderSource } from "./FolderBrowser";

const spaces = [create(SpaceSchema, { spaceId: "general", name: "General", defaultForNew: true }), create(SpaceSchema, { spaceId: "media", name: "Media", spanNodes: true })];
const volumes = [
  { name: "config", path: "/config" },
  { name: "media", path: "/media", shared: true },
];

function Harness({ onValue }: { onValue: (f: Folders) => void }) {
  const tree: Record<string, string[]> = { "media:": ["Movies", "Shows"], "media:Shows": [] };
  const source = (spaceId: string): FolderSource => ({
    key: spaceId,
    list: async (p) => tree[`${spaceId}:${p}`] ?? [],
    make: async (p) => {
      const parent = p.split("/").slice(0, -1).join("/");
      tree[`${spaceId}:${parent}`] = [...(tree[`${spaceId}:${parent}`] ?? []), p.split("/").pop()!];
    },
  });
  const [v, setV] = useState(() => defaultFolders(volumes, spaces, "general"));
  return (
    <FoldersEditor
      volumes={volumes}
      spaces={spaces}
      value={v}
      onChange={(f) => {
        setV(f);
        onValue(f);
      }}
      source={source}
      serviceName="jellyfin"
    />
  );
}

describe("FoldersEditor", () => {
  it("suggests the shared volume's Space, picks folders, makes one, and adds an extra folder", async () => {
    let last: Folders | undefined;
    render(
      <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
        <Harness onValue={(f) => (last = f)} />
      </QueryClientProvider>,
    );
    // Decision 175: media points at the top of Media; config has its own folder.
    expect(screen.getByRole("button", { name: /^Media/ })).toBeTruthy();
    expect(screen.getByRole("radio", { name: "Service's folder", checked: true })).toBeTruthy();

    // Point media at Media/Movies.
    fireEvent.click(screen.getByRole("button", { name: /^Media/ }));
    let dialog = await screen.findByRole("dialog");
    fireEvent.click(await within(dialog).findByRole("button", { name: "Movies" }));
    fireEvent.click(within(dialog).getByRole("button", { name: "Use this folder" }));
    expect(last?.volumeFolders).toEqual({ media: "Movies" });

    // An extra folder: Shows/2026, made in the browser, read only.
    fireEvent.click(screen.getByRole("button", { name: /Add a folder/ }));
    dialog = await screen.findByRole("dialog");
    fireEvent.click(await within(dialog).findByRole("button", { name: "Shows" }));
    fireEvent.click(within(dialog).getByRole("button", { name: /New folder/ }));
    fireEvent.change(within(dialog).getByLabelText("New folder name"), { target: { value: "2026" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Make" }));
    await within(dialog).findByText((_, el) => el?.tagName === "P" && el.textContent === "Uses Media / Shows / 2026");
    expect((within(dialog).getByLabelText("Inside the container at") as HTMLInputElement).value).toBe("/mnt/2026");
    fireEvent.change(within(dialog).getByLabelText("Inside the container at"), { target: { value: "/media" } });
    expect(within(dialog).getByText("Already used inside the container")).toBeTruthy();
    fireEvent.change(within(dialog).getByLabelText("Inside the container at"), { target: { value: "/media/new" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Use this folder" }));
    expect(last?.mounts).toEqual([{ spaceId: "media", folder: "Shows/2026", path: "/media/new", readOnly: false }]);
  });
});
