import { createPrivateKey, generateKeyPairSync, sign, type webcrypto } from "node:crypto";
import { readFile, writeFile, unlink } from "node:fs/promises";
import { join } from "node:path";

type JsonWebKey = webcrypto.JsonWebKey;

export interface Identity {
  pubkey: string; // base64url of the raw 32-byte Ed25519 public key (the JWK `x`)
  jwk: JsonWebKey;
}

// The keypair is the boat's identity with aiscast. It is generated once and never leaves the data dir.
export async function loadIdentity(dir: string): Promise<Identity> {
  const file = join(dir, "identity.json");
  try {
    const jwk = JSON.parse(await readFile(file, "utf8")) as JsonWebKey;
    if (jwk.kty === "OKP" && jwk.crv === "Ed25519" && jwk.x && jwk.d) return { pubkey: jwk.x, jwk };
  } catch {
    // first start, or unreadable: generate below
  }
  const { privateKey } = generateKeyPairSync("ed25519");
  const jwk = privateKey.export({ format: "jwk" }) as JsonWebKey;
  await writeFile(file, JSON.stringify(jwk), { mode: 0o600 });
  return { pubkey: jwk.x!, jwk };
}

export interface Token {
  token: string;
  exp: number; // unix seconds; 0 = never
  pubkey: string;
  server: string;
  signed?: boolean; // the server confirmed it checked the signature; until one does, the plugin mints again each start
  vesselName?: string; // the vessel name sent with the mint; a different one now means minting again
}

const RENEW_BEFORE = 3 * 24 * 3600; // seconds before expiry at which a new token is requested

// tokenSub reads the station a token names (`ak1.<base64url claims>.<signature>`), which is what aiscast
// files its publishes under; unverified here, since only the server's opinion of it matters.
export function tokenSub(token: string): string | null {
  try {
    const claims = JSON.parse(Buffer.from(token.split(".")[1] ?? "", "base64url").toString()) as { sub?: unknown };
    return typeof claims.sub === "string" ? claims.sub : null;
  } catch {
    return null;
  }
}

// A personal token from POST /v1/keys, cached in the data dir and renewed a few days before it expires, or
// when the vessel name it was minted with is no longer the one to send.
export async function loadToken(
  dir: string,
  server: string,
  identity: Identity,
  vesselName: string,
  now = Date.now(),
): Promise<Token & { nameError?: string }> {
  const file = join(dir, "token.json");
  try {
    const t = JSON.parse(await readFile(file, "utf8")) as Token;
    // exp 0 = the token never expires (personal tokens are revoked, not expired)
    const fresh = t.exp === 0 || t.exp - now / 1000 > RENEW_BEFORE;
    if (t.pubkey === identity.pubkey && t.server === server && fresh && t.signed && (t.vesselName ?? "") === vesselName) return t;
  } catch {
    // none cached
  }
  const t = await mintToken(server, identity, vesselName, now);
  const { nameError: _, ...cached } = t; // reported once, not on every start
  await writeFile(file, JSON.stringify(cached), { mode: 0o600 });
  return t;
}

export async function forgetToken(dir: string): Promise<void> {
  await unlink(join(dir, "token.json")).catch(() => {});
}

// The lines a mint request signs, as aiscast checks them: no newline at the end, a blank line for no name.
export function mintMessage(pubkey: string, ts: number, bindIP: boolean, name: string, vesselName: string): Buffer {
  return Buffer.from(["aiscast-key", pubkey, String(ts), bindIP ? "1" : "0", name, vesselName].join("\n"));
}

// Mints a token, signing the request with the boat's key so nobody else can mint one for it. The vessel name
// names the station after the boat; an empty one clears it. A name aiscast will not take comes back as
// nameError, and the token is issued anyway.
export async function mintToken(
  server: string,
  identity: Identity,
  vesselName: string,
  now = Date.now(),
): Promise<Token & { nameError?: string }> {
  const ts = Math.floor(now / 1000);
  const key = createPrivateKey({ key: identity.jwk, format: "jwk" });
  const sig = sign(null, mintMessage(identity.pubkey, ts, false, "", vesselName), key).toString("base64url");
  const res = await fetch(new URL("/v1/keys", server), {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ pubkey: identity.pubkey, vessel_name: vesselName, ts, sig }),
    signal: AbortSignal.timeout(15_000),
  });
  if (!res.ok) throw new Error(`POST /v1/keys: ${res.status} ${(await res.text()).trim()}`);
  const body = (await res.json()) as { token: string; claims: { exp?: number }; signed?: boolean; name_error?: string };
  return {
    token: body.token,
    exp: body.claims.exp ?? 0,
    pubkey: identity.pubkey,
    server,
    // A server older than signed requests ignores sig and still issues a token. Only its confirmation counts,
    // so a plugin released first mints again after the server upgrade and gets its name and key lock then.
    signed: body.signed === true,
    vesselName,
    ...(body.name_error ? { nameError: body.name_error } : {}),
  };
}
