import { createPrivateKey, generateKeyPairSync, randomUUID, sign } from "node:crypto";

/**
 * The tests' token and the issuer that signs it, minted once per run. Every browser, render
 * and test request comes from 127.0.0.1, and the server's per-address limit of 120 requests a
 * minute would start refusing them part way through the suite. A partner token with `rpm: 0`
 * has no request limit. The config runs in the runner and again in each worker, and the
 * workers inherit the runner's environment, so all of them share the one set in the runner.
 */
export function e2eAuth(): { token: string; issuer: string } {
  if (!process.env.AISCAST_E2E_TOKEN || !process.env.AISCAST_E2E_ISSUER || !process.env.AISCAST_E2E_KEY) {
    const { publicKey, privateKey } = generateKeyPairSync("ed25519");
    process.env.AISCAST_E2E_KEY = privateKey.export({ format: "pem", type: "pkcs8" }).toString();
    process.env.AISCAST_E2E_TOKEN = mint({ sub: "e2e" });
    process.env.AISCAST_E2E_ISSUER = `e2e:${publicKey.export({ format: "jwk" }).x}`;
  }
  return { token: process.env.AISCAST_E2E_TOKEN!, issuer: process.env.AISCAST_E2E_ISSUER! };
}

/**
 * A token the server lets hold one stream at a time, so a browser that opened a second would be
 * refused it. Each is its own subscriber, as tests running side by side must not share one.
 */
export function oneStreamToken(): string {
  e2eAuth();
  return mint({ sub: `e2e-one-stream-${randomUUID()}`, conns: 1 });
}

function mint(claims: object): string {
  const key = createPrivateKey(process.env.AISCAST_E2E_KEY!);
  const body = `ak1.${Buffer.from(JSON.stringify({ kid: "e2e", role: "partner", iat: Math.floor(Date.now() / 1000), rpm: 0, ...claims })).toString("base64url")}`;
  return `${body}.${sign(null, Buffer.from(body), key).toString("base64url")}`;
}
