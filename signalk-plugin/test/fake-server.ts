import { createServer, type Server } from "node:http";
import { type AddressInfo } from "node:net";
import { WebSocketServer, type WebSocket } from "ws";

export interface FakeServer {
  url: string; // http://127.0.0.1:port
  frames: Record<string, unknown>[]; // every frame any client sent
  keyRequests: { pubkey: string }[];
  clients: WebSocket[];
  ack: boolean; // answer publish frames with ack (default true)
  keysStatus: number; // response code for POST /v1/keys
  send(frame: unknown): void; // to every connected client
  waitForFrame(pred: (f: Record<string, unknown>) => boolean, timeoutMs?: number): Promise<Record<string, unknown>>;
  close(): Promise<void>;
}

// Enough of aiscast to drive the plugin: POST /v1/keys and a /v1/stream socket that records frames and acks.
export function startFakeServer(port = 0): Promise<FakeServer> {
  const frames: Record<string, unknown>[] = [];
  const keyRequests: { pubkey: string }[] = [];
  const clients: WebSocket[] = [];
  const waiters: { pred: (f: Record<string, unknown>) => boolean; resolve: (f: Record<string, unknown>) => void }[] = [];

  const http: Server = createServer((req, res) => {
    if (req.method === "POST" && req.url === "/v1/keys") {
      let body = "";
      req.on("data", (c) => (body += c));
      req.on("end", () => {
        keyRequests.push(JSON.parse(body));
        res.statusCode = fake.keysStatus;
        res.setHeader("content-type", "application/json");
        res.end(
          fake.keysStatus === 200
            ? JSON.stringify({ token: "ak1.test.token", claims: { exp: Math.floor(Date.now() / 1000) + 30 * 86400 } })
            : JSON.stringify({ error: "no personal issuer" }),
        );
      });
      return;
    }
    res.statusCode = 404;
    res.end();
  });
  const wss = new WebSocketServer({ server: http, path: "/v1/stream" });
  wss.on("connection", (ws) => {
    clients.push(ws);
    ws.on("message", (data) => {
      const f = JSON.parse(data.toString()) as Record<string, unknown>;
      frames.push(f);
      if (f.type === "publish" && fake.ack) ws.send(JSON.stringify({ type: "ack", n: (f.nmea as string[]).length }));
      for (const w of waiters.splice(0)) {
        if (w.pred(f)) w.resolve(f);
        else waiters.push(w);
      }
    });
    ws.on("close", () => clients.splice(clients.indexOf(ws), 1));
  });

  const fake: FakeServer = {
    url: "",
    frames,
    keyRequests,
    clients,
    ack: true,
    keysStatus: 200,
    send(frame) {
      for (const c of clients) c.send(JSON.stringify(frame));
    },
    waitForFrame(pred, timeoutMs = 5000) {
      const hit = frames.find(pred);
      if (hit) return Promise.resolve(hit);
      return new Promise((resolve, reject) => {
        const t = setTimeout(() => reject(new Error("timed out waiting for frame")), timeoutMs);
        waiters.push({
          pred,
          resolve: (f) => {
            clearTimeout(t);
            resolve(f);
          },
        });
      });
    },
    close() {
      for (const c of clients) c.terminate();
      wss.close();
      return new Promise((resolve) => http.close(() => resolve()));
    },
  };

  return new Promise((resolve) => {
    http.listen(port, "127.0.0.1", () => {
      fake.url = `http://127.0.0.1:${(http.address() as AddressInfo).port}`;
      resolve(fake);
    });
  });
}
