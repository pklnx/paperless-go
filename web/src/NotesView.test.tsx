import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { NotesBoundary, NotesView } from "./NotesView";

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

function Throws(): never {
  throw new Error("render failed");
}

describe("NotesView", () => {
  it("renders the notes' Markdown", () => {
    render(<NotesView text={"# Titel\n**fett**"} />);
    expect(screen.getByRole("heading", { level: 3 }).textContent).toBe("Titel");
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
