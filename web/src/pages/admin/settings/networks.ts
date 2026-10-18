/*
 * Private networks for teams' own model providers and MCP servers: CIDRs checked before Save (an
 * IPv4 or IPv6 address with a prefix length). The server has the last word:
 * it refuses public, loopback and link-local ranges (400
 * invalid_private_networks).
 */

/** The API's limit. */
export const maxNetworks = 50;

const ipv4 = (s: string) => {
  const parts = s.split(".");
  return parts.length === 4 && parts.every((p) => /^\d{1,3}$/.test(p) && Number(p) <= 255 && (p === "0" || !p.startsWith("0")));
};

const hextets = (s: string) => s === "" || s.split(":").every((g) => /^[0-9a-f]{1,4}$/i.test(g));

function ipv6(s: string) {
  // An IPv4 tail (::ffff:10.0.0.1) counts as two groups.
  let addr = s;
  const lastColon = addr.lastIndexOf(":");
  if (addr.slice(lastColon + 1).includes(".")) {
    if (!ipv4(addr.slice(lastColon + 1))) return false;
    addr = addr.slice(0, lastColon + 1) + "0:0";
  }
  const halves = addr.split("::");
  if (halves.length > 2) return false;
  if (halves.length === 1) return hextets(addr) && addr.split(":").length === 8;
  const [head, tail] = halves as [string, string];
  if (!hextets(head) || !hextets(tail)) return false;
  const count = (head ? head.split(":").length : 0) + (tail ? tail.split(":").length : 0);
  return count <= 7;
}

/** "10.20.0.0/16" and "fd00::/8" are CIDRs; "10.20.0.0", "10.0.0.0/33" and "example.com" aren't. */
export function isCidr(value: string): boolean {
  const m = /^([^/\s]+)\/(\d{1,3})$/.exec(value.trim());
  if (!m) return false;
  const [, addr, bits] = m as unknown as [string, string, string];
  if (ipv4(addr)) return Number(bits) <= 32;
  return addr.includes(":") && ipv6(addr) && Number(bits) <= 128;
}

/** What's wrong with the list, before it reaches the server. */
export function networksProblem(list: string[]): string | undefined {
  const bad = list.filter((n) => !isCidr(n));
  if (bad.length === 1) return `${bad[0]} isn't a network: write an address and a prefix length, such as 10.20.0.0/16 or fd00:10::/32.`;
  if (bad.length > 1) return `These aren't networks: ${bad.join(", ")}. Write an address and a prefix length, such as 10.20.0.0/16.`;
  if (list.length > maxNetworks) return `At most ${maxNetworks} networks.`;
  return undefined;
}
