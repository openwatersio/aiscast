import { generateKeyPairSync, type JsonWebKey } from "node:crypto";
import { readFile, writeFile, unlink } from "node:fs/promises";
import { join } from "node:path";

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
  exp: number; // unix seconds
  pubkey: string;
  server: string;
}

const RENEW_BEFORE = 3 * 24 * 3600; // seconds before expiry at which a new token is requested

// A personal token from POST /v1/keys, cached in the data dir and renewed a few days before it expires.
export async function loadToken(dir: string, server: string, pubkey: string, now = Date.now()): Promise<Token> {
  const file = join(dir, "token.json");
  try {
    const t = JSON.parse(await readFile(file, "utf8")) as Token;
    if (t.pubkey === pubkey && t.server === server && t.exp - now / 1000 > RENEW_BEFORE) return t;
  } catch {
    // none cached
  }
  const t = await mintToken(server, pubkey);
  await writeFile(file, JSON.stringify(t), { mode: 0o600 });
  return t;
}

export async function forgetToken(dir: string): Promise<void> {
  await unlink(join(dir, "token.json")).catch(() => {});
}

export async function mintToken(server: string, pubkey: string): Promise<Token> {
  const res = await fetch(new URL("/v1/keys", server), {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ pubkey }),
    signal: AbortSignal.timeout(15_000),
  });
  if (!res.ok) throw new Error(`POST /v1/keys: ${res.status} ${(await res.text()).trim()}`);
  const body = (await res.json()) as { token: string; claims: { exp: number } };
  return { token: body.token, exp: body.claims.exp, pubkey, server };
}
