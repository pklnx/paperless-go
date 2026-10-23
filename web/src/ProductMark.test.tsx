import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render } from "@testing-library/react";
import markDark from "./brand/paperless-go-mark-dark.svg";
import markLight from "./brand/paperless-go-mark-light.svg";
import { ProductMark } from "./ProductMark";

afterEach(cleanup);

describe("ProductMark", () => {
  it("renders a decorative light and dark mark, switched by the dark variant", () => {
    const { container } = render(<ProductMark className="size-7" />);
    const imgs = [...container.querySelectorAll("img")];
    expect(imgs).toHaveLength(2);
    for (const img of imgs) {
      expect(img.getAttribute("alt")).toBe("");
      expect(img.classList).toContain("size-7");
    }
    const [light, dark] = imgs as [HTMLImageElement, HTMLImageElement];
    expect(light.classList).toContain("app-mark--light");
    expect(light.classList).toContain("dark:hidden");
    expect(light.getAttribute("src")).toBe(markLight);
    expect(dark.classList).toContain("app-mark--dark");
    expect(dark.classList).toContain("hidden");
    expect(dark.classList).toContain("dark:block");
    expect(dark.getAttribute("src")).toBe(markDark);
    expect(markLight).not.toBe(markDark);
  });
});
