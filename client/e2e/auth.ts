import { generateKeyPairSync, sign } from "node:crypto";

/**
 * The tests' token and the issuer that signs it, minted once per run. Every browser, render
 * and test request comes from 127.0.0.1, and the server's per-address limit of 120 requests a
 * minute would start refusing them part way through the suite. A partner token with `rpm: 0`
 * has no request limit. The config runs in the runner and again in each worker, and the
 * workers inherit the runner's environment, so all of them share the one set in the runner.
 */
export function e2eAuth(): { token: string; issuer: string } {
  if (!process.env.AISCAST_E2E_TOKEN || !process.env.AISCAST_E2E_ISSUER) {
    const { publicKey, privateKey } = generateKeyPairSync("ed25519");
    const claims = { kid: "e2e", sub: "e2e", role: "partner", iat: Math.floor(Date.now() / 1000), rpm: 0 };
    const body = `ak1.${Buffer.from(JSON.stringify(claims)).toString("base64url")}`;
    const sig = sign(null, Buffer.from(body), privateKey).toString("base64url");
    process.env.AISCAST_E2E_TOKEN = `${body}.${sig}`;
    process.env.AISCAST_E2E_ISSUER = `e2e:${publicKey.export({ format: "jwk" }).x}`;
  }
  return { token: process.env.AISCAST_E2E_TOKEN!, issuer: process.env.AISCAST_E2E_ISSUER! };
}
