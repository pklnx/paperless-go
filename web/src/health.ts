export type Health = "checking" | "ok" | "down";

/** Asks the server's /healthz, which answers 200 while the database answers. */
export async function fetchHealth(fetcher: typeof fetch = fetch): Promise<Health> {
  try {
    const res = await fetcher("/healthz", { cache: "no-store" });
    return res.ok ? "ok" : "down";
  } catch {
    return "down"; // server not reachable
  }
}
