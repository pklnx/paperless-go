// @vitest-environment node
// The Carbon and IBM Plex packages run `ibmtelemetry` (@ibm/telemetry-js)
// after install, which reports usage to IBM. web/pnpm-workspace.yaml blocks
// the install scripts of every such package, strictDepBuilds makes a new
// dependency with a build script fail the install until it is listed, and
// IBM_TELEMETRY_DISABLED=true is the second guard wherever pnpm installs
// (CI, the Dockerfile's web stage, the Makefile). This test fails without
// the block, and when a Carbon upgrade brings a package that is not listed.
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

const root = fileURLToPath(new URL("../../", import.meta.url));
const read = (path: string) => readFileSync(`${root}${path}`, "utf8");

/** Every @carbon/* and @ibm/* package the lockfile resolves. */
function lockedPackages(): string[] {
  const names = new Set<string>();
  for (const m of read("web/pnpm-lock.yaml").matchAll(/^ {2}'?(@(?:carbon|ibm)\/[^@'\s]+)@/gm)) names.add(m[1]!);
  return [...names].sort();
}

/** The allowBuilds entries of pnpm-workspace.yaml, by package name. */
function allowBuilds(): Record<string, string> {
  const yaml = read("web/pnpm-workspace.yaml");
  const section = yaml.match(/^allowBuilds:\n((?: {2}.*\n)+)/m);
  const out: Record<string, string> = {};
  for (const m of (section?.[1] ?? "").matchAll(/^ {2}"?([^":\n]+)"?:\s*(\S+)\s*$/gm)) out[m[1]!] = m[2]!;
  return out;
}

describe("the IBM telemetry block", () => {
  it("lists every @carbon and @ibm package of the lockfile with allowBuilds: false", () => {
    const locked = lockedPackages();
    expect(locked).toContain("@carbon/react");
    expect(locked).toContain("@ibm/telemetry-js");
    const allowed = allowBuilds();
    for (const name of locked) expect(allowed[name], `${name} in web/pnpm-workspace.yaml allowBuilds`).toBe("false");
    // Nothing is allowed to build.
    expect(Object.values(allowed).every((v) => v === "false")).toBe(true);
  });

  it("fails the install on a new dependency with a build script", () => {
    expect(read("web/pnpm-workspace.yaml")).toMatch(/^strictDepBuilds: true$/m);
  });

  it("sets IBM_TELEMETRY_DISABLED before every pnpm install", () => {
    // The Dockerfile's web stage: the variable before its install.
    const dockerfile = read("Dockerfile");
    const stage = dockerfile.slice(dockerfile.indexOf("AS web"), dockerfile.indexOf("\nFROM ", dockerfile.indexOf("AS web")));
    const env = stage.indexOf("ENV IBM_TELEMETRY_DISABLED=true");
    const install = stage.indexOf("pnpm install");
    expect(env, "ENV IBM_TELEMETRY_DISABLED=true in the web stage").toBeGreaterThanOrEqual(0);
    expect(install).toBeGreaterThan(env);
    // The workspace file with the block is copied before the install.
    expect(stage.indexOf("pnpm-workspace.yaml")).toBeLessThan(install);
    // The Makefile exports it for every recipe; CI sets it in the jobs that install.
    expect(read("Makefile")).toMatch(/^export IBM_TELEMETRY_DISABLED := true$/m);
    const ci = read(".github/workflows/ci.yml");
    const jobs = ci.split(/^  (?=[a-z-]+:$)/m).filter((j) => j.includes("pnpm install") || j.includes("pnpm audit"));
    expect(jobs.length).toBeGreaterThanOrEqual(3);
    for (const job of jobs) expect(job, job.split("\n")[0]).toContain('IBM_TELEMETRY_DISABLED: "true"');
  });
});
