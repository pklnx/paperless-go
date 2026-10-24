import { afterEach, describe, expect, it } from "vitest";
import { StrictMode } from "react";
import { cleanup, render, screen, within } from "@testing-library/react";
import { DangerModal } from "./DangerModal";

// The modal's focus handling on its own: the page around it is a button
// that something may move the focus to while the modal is open. StrictMode
// at the root, as main.tsx has it: in development React runs every effect's
// cleanup once without unmounting, which must not switch the trap off.

afterEach(cleanup);

function Page() {
  return (
    <>
      <button type="button">Elsewhere</button>
      <DangerModal modalHeading="Discard?" primaryButtonText="Discard" secondaryButtonText="Keep" onRequestClose={() => {}}>
        <p>Sure?</p>
      </DangerModal>
    </>
  );
}

describe("AppModal's focus", () => {
  it.each([false, true])("stays in the modal when something moves it out (StrictMode: %s)", (strict) => {
    render(
      strict ? (
        <StrictMode>
          <Page />
        </StrictMode>
      ) : (
        <Page />
      ),
    );
    const ask = screen.getByRole("dialog", { name: "Discard?" });
    const keep = within(ask).getByRole("button", { name: "Keep" });
    expect(document.activeElement).toBe(keep);
    screen.getByRole("button", { name: "Elsewhere" }).focus();
    expect(document.activeElement).toBe(keep);
  });
});
