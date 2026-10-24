import markDark from "./brand/paperless-go-mark-dark.svg";
import markLight from "./brand/paperless-go-mark-light.svg";
import { useTheme } from "./theme";

/**
 * The paperless-go mark (pklnx design system, BRAND-ASSETS.md) for the
 * applied theme: the light mark on Carbon White, the dark one on G100. It
 * follows the theme toggle and the system. Decorative: the app name next to
 * it is the accessible name. The files are used unchanged, never recoloured.
 */
export function ProductMark({ className, size }: { className?: string; size?: number }) {
  const theme = useTheme();
  return (
    <img
      src={theme === "dark" ? markDark : markLight}
      alt=""
      width={size}
      height={size}
      className={["app-mark", `app-mark--${theme}`, className].filter(Boolean).join(" ")}
    />
  );
}
