import markDark from "./brand/paperless-go-mark-dark.svg";
import markLight from "./brand/paperless-go-mark-light.svg";

/**
 * The paperless-go mark, drawn for light and dark grounds. Decorative: the
 * app name next to it is the accessible name. The class-based dark variant
 * (html.dark, see theme.ts) shows the one that fits the current theme.
 */
export function ProductMark({ className }: { className: string }) {
  return (
    <>
      <img src={markLight} alt="" className={`app-mark app-mark--light dark:hidden ${className}`} />
      <img src={markDark} alt="" className={`app-mark app-mark--dark hidden dark:block ${className}`} />
    </>
  );
}
