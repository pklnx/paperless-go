import { describe, expect, it } from "vitest";
import { nameCompare, nameTree, sortByName, type NameNode } from "./collate";

const words = ["Zebra", "Öl", "Äpfel", "apfel", "Apfel", "Bank", "Ofen"];
const sorted = ["apfel", "Apfel", "Äpfel", "Bank", "Ofen", "Öl", "Zebra"];
const id = (s: string) => s;

describe("sortByName", () => {
  for (const l of ["de", "en"] as const) {
    it(`puts Ä next to A in ${l}, whatever the input order`, () => {
      expect(sortByName(words, id, l)).toEqual(sorted);
      expect(sortByName([...words].reverse(), id, l)).toEqual(sorted);
      // Code point order would put the umlauts after Z.
      expect(sortByName(words, id, l).indexOf("Äpfel")).toBeLessThan(sortByName(words, id, l).indexOf("Zebra"));
    });

    it(`sorts numbers numerically in ${l}`, () => {
      expect(sortByName(["Rechnung 10", "Rechnung 2", "Rechnung 1"], id, l)).toEqual(["Rechnung 1", "Rechnung 2", "Rechnung 10"]);
    });

    it(`gives user names one fixed order in ${l}`, () => {
      const names = ["b", "ab", "a_c", "a.b", "a-b", "a10", "a2", "a"];
      const want = ["a", "a_c", "a-b", "a.b", "a2", "a10", "ab", "b"];
      expect(sortByName(names, id, l)).toEqual(want);
      expect(sortByName([...names].reverse(), id, l)).toEqual(want);
    });

    it(`orders NFC and NFD forms the same from both input orders in ${l}`, () => {
      const nfc = "Ä";
      const nfd = "Ä";
      expect(nameCompare(l)(nfc, nfd)).not.toBe(0);
      expect(sortByName([nfc, nfd], id, l)).toEqual(sortByName([nfd, nfc], id, l));
    });
  }

  it("leaves its input unchanged", () => {
    const input = ["b", "a"];
    const out = sortByName(input, id, "en");
    expect(input).toEqual(["b", "a"]);
    expect(out).not.toBe(input);
  });
});

interface Tag {
  id: string;
  name: string;
  parentId: string | null;
}
const tag = (i: string, name: string, parentId: string | null = null): Tag => ({ id: i, name, parentId });
const shape = (nodes: NameNode<Tag>[]): unknown[] => nodes.map((n) => (n.children.length ? [n.item.name, shape(n.children)] : n.item.name));
const flat = (nodes: NameNode<Tag>[]): string[] => nodes.flatMap((n) => [n.item.id, ...flat(n.children)]);

describe("nameTree", () => {
  it("sorts every level", () => {
    const tags = [tag("1", "Steuer"), tag("2", "2024", "1"), tag("3", "2023", "1"), tag("4", "Belege", "1"), tag("5", "Ärzte")];
    expect(shape(nameTree(tags, (x) => x.name, "de"))).toEqual(["Ärzte", ["Steuer", ["2023", "2024", "Belege"]]]);
  });

  it("makes a node with a missing parent a root", () => {
    const tags = [tag("1", "B"), tag("2", "A", "gone")];
    expect(shape(nameTree(tags, (x) => x.name, "en"))).toEqual(["A", "B"]);
  });

  it("keeps a fixed order for siblings that differ only in case", () => {
    const a = [tag("1", "Bank"), tag("2", "bank")];
    expect(shape(nameTree(a, (x) => x.name, "en"))).toEqual(["bank", "Bank"]);
    expect(shape(nameTree([...a].reverse(), (x) => x.name, "en"))).toEqual(["bank", "Bank"]);
  });

  it("shows every node of a cycle exactly once", () => {
    const tags = [tag("a", "A", "b"), tag("b", "B", "a"), tag("c", "C", "c"), tag("d", "D", "c")];
    const ids = flat(nameTree(tags, (x) => x.name, "en"));
    expect([...ids].sort()).toEqual(["a", "b", "c", "d"]);
  });
});
