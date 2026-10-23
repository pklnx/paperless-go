import { afterEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, render, screen } from "@testing-library/react";
import { NotesBoundary, NotesView } from "./NotesView";

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.doUnmock("./htmlEntities");
  vi.resetModules();
});

function Throws(): never {
  throw new Error("render failed");
}

describe("NotesView", () => {
  it("renders the notes' Markdown", () => {
    render(<NotesView text={"# Titel\n**fett**"} />);
    expect(screen.getByRole("heading", { level: 3 }).textContent).toBe("Titel");
  });

  it("loads the entity table only for a note with &name;, shows it as written until then, then renders again (#134)", async () => {
    vi.resetModules();
    let imported = 0;
    let release!: () => void;
    const gate = new Promise<void>((r) => (release = r));
    vi.doMock("./htmlEntities", async () => {
      imported++;
      await gate;
      return { namedEntities: { amp: "&", uuml: "ü" } };
    });
    const { NotesView: Fresh } = await import("./NotesView");
    const markdown = await import("./markdown");
    // No named entity: nothing is loaded; numeric entities need no table.
    const { container, rerender } = render(<Fresh text={"**a** &#228; & b"} />);
    await act(async () => {});
    expect(container.textContent).toBe("a ä & b");
    expect(imported).toBe(0);
    // A named entity: shown as written while the table loads.
    rerender(<Fresh text={"**M&uuml;ller** &amp; https://a.example/?x=1&amp;y=2"} />);
    expect(container.textContent).toBe("M&uuml;ller &amp; https://a.example/?x=1&amp;y=2");
    expect(container.querySelector("strong")?.textContent).toBe("M&uuml;ller");
    expect(container.querySelector("a")?.getAttribute("href")).toBe("https://a.example/?x=1&amp;y=2");
    release();
    await screen.findByText("Müller");
    expect(container.textContent).toBe("Müller & https://a.example/?x=1&y=2");
    expect(container.querySelector("a")?.getAttribute("href")).toBe("https://a.example/?x=1&y=2");
    expect(markdown.entitiesLoaded()).toBe(true);
    // Loaded once: other notes render decoded at once.
    rerender(<Fresh text={"&uuml;"} />);
    expect(container.textContent).toBe("ü");
    expect(imported).toBe(1);
  });

  it("shows the notes as written when rendering throws, and tries again for other text", () => {
    // React reports the caught error on the console.
    vi.spyOn(console, "error").mockImplementation(() => {});
    const { container, rerender } = render(
      <NotesBoundary text="**roh**">
        <Throws />
      </NotesBoundary>,
    );
    expect(container.textContent).toBe("**roh**");
    rerender(
      <NotesBoundary text="neu">
        <b>ok</b>
      </NotesBoundary>,
    );
    expect(container.textContent).toBe("ok");
  });
});
