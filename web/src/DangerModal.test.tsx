import { afterEach, describe, expect, it } from "vitest";
import { StrictMode, useState } from "react";
import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { AppModal, DangerModal } from "./DangerModal";

// The modals on their own: while one is open, the rest of document.body
// (the page, and any modal below it) is inert, and it is usable again
// when the modal closes, before the focus goes back. StrictMode at the
// root, as main.tsx has it: in development React runs every effect's
// cleanup once without unmounting, which must leave the same state.

afterEach(cleanup);

/** A page with a button, a danger modal, and a second modal that opens above it from the first's primary button. */
function Page() {
  const [second, setSecond] = useState(false);
  return (
    <>
      <button type="button">Elsewhere</button>
      <DangerModal modalHeading="Discard?" primaryButtonText="Discard" secondaryButtonText="Keep" onRequestSubmit={() => setSecond(true)} onRequestClose={() => {}}>
        <p>Sure?</p>
      </DangerModal>
      {second && (
        <AppModal modalHeading="Above" primaryButtonText="OK" secondaryButtonText="Cancel" onRequestClose={() => setSecond(false)} onSecondarySubmit={() => setSecond(false)}>
          <input data-modal-primary-focus="" aria-label="Field" />
        </AppModal>
      )}
    </>
  );
}

const inert = (el: Element) => el.hasAttribute("inert");

describe("AppModal and the rest of the page", () => {
  it.each([false, true])("makes the page inert while open, and usable again before the focus goes back (StrictMode: %s)", (strict) => {
    const elsewhere = () => screen.getByRole("button", { name: "Elsewhere" });
    const { container, unmount } = render(
      strict ? (
        <StrictMode>
          <Page />
        </StrictMode>
      ) : (
        <Page />
      ),
    );
    const ask = screen.getByRole("dialog", { name: "Discard?" });
    expect(document.activeElement).toBe(within(ask).getByRole("button", { name: "Keep" }));
    // The page is inert, the modal is not.
    expect(inert(container)).toBe(true);
    expect(elsewhere().closest("[inert]")).toBe(container);
    expect(ask.closest("[inert]")).toBeNull();

    // A second modal above: the first is inert too, with the focus in the second.
    fireEvent.click(within(ask).getByRole("button", { name: "Discard" }));
    const above = screen.getByRole("dialog", { name: "Above" });
    expect(document.activeElement).toBe(within(above).getByLabelText("Field"));
    // The attribute sits on the modal's root in document.body.
    expect(ask.closest("[inert]")).toBe(ask.closest(".cds--modal"));
    expect(above.closest("[inert]")).toBeNull();
    expect(inert(container)).toBe(true);

    // The second closes: the first is usable again, and the focus is back where it was in it.
    fireEvent.click(within(above).getByRole("button", { name: "Cancel" }));
    expect(screen.queryByRole("dialog", { name: "Above" })).toBeNull();
    expect(ask.closest("[inert]")).toBeNull();
    expect(document.activeElement).toBe(within(ask).getByRole("button", { name: "Keep" }));
    expect(inert(container)).toBe(true);

    // All closed: nothing is inert any more.
    unmount();
    expect(inert(container)).toBe(false);
  });

  it("leaves an inert attribute it did not set", () => {
    const other = document.createElement("div");
    other.setAttribute("inert", "");
    document.body.append(other);
    try {
      const { unmount } = render(<Page />);
      expect(inert(other)).toBe(true);
      unmount();
      expect(inert(other)).toBe(true);
    } finally {
      other.remove();
    }
  });
});
