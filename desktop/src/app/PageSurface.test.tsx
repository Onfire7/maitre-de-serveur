// @vitest-environment jsdom
import { afterEach, describe, expect, it } from "vitest";
import { act, cleanup, render, screen } from "@testing-library/react";
import { PageSurface } from "./PageSurface";
import { modalClosed, modalOpened, openPanel, resetLayout } from "./layout";
import { Dialog, DialogContent, DialogTitle } from "@/components/ui/dialog";

// Decision 293: the surface moves without resizing, and a modal replaces
// the page with its stand-in.
describe("PageSurface", () => {
  afterEach(() => {
    cleanup();
    resetLayout();
  });

  it("translates by the panel width and keeps its size", () => {
    render(
      <PageSurface>
        <p>page</p>
      </PageSurface>,
    );
    const s = screen.getByTestId("page-surface");
    expect(s.style.transform).toBe("translateX(0px)");
    act(() => openPanel(240));
    expect(s.style.transform).toBe("translateX(240px)");
    expect(s.className).toContain("inset-0");
    expect(s.style.width).toBe("");
  });

  it("hides the page behind the stand-in while a modal is open", () => {
    render(
      <PageSurface>
        <p>page</p>
      </PageSurface>,
    );
    expect(screen.queryByTestId("page-backdrop")).toBeNull();
    act(() => modalOpened());
    expect(screen.getByTestId("page-backdrop")).toBeTruthy();
    expect(screen.getByText("page").parentElement!.className).toContain("invisible");
    act(() => modalClosed());
    expect(screen.queryByTestId("page-backdrop")).toBeNull();
  });

  it("a Dialog's content marks a modal for as long as it is open", () => {
    const { rerender } = render(
      <>
        <PageSurface>
          <p>page</p>
        </PageSurface>
        <Dialog open>
          <DialogContent>
            <DialogTitle>Hi</DialogTitle>
          </DialogContent>
        </Dialog>
      </>,
    );
    expect(screen.getByTestId("page-backdrop")).toBeTruthy();
    rerender(
      <>
        <PageSurface>
          <p>page</p>
        </PageSurface>
        <Dialog open={false}>
          <DialogContent>
            <DialogTitle>Hi</DialogTitle>
          </DialogContent>
        </Dialog>
      </>,
    );
    expect(screen.queryByTestId("page-backdrop")).toBeNull();
  });
});
