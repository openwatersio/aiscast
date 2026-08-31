import type { EventEmitter } from "node:events";
import type { Plugin, ServerAPI } from "@signalk/server-api";
import { Downlink, type ReceiveMode } from "./downlink.js";
import {
  forgetToken,
  loadIdentity,
  loadToken,
  type Token,
} from "./identity.js";
import { Link } from "./link.js";
import { n2kToSentence } from "./n2k.js";
import { isAis, isOwnShip } from "./nmea.js";
import { OwnShip, SOURCE_TAG } from "./ownship.js";
import { Uplink } from "./uplink.js";

export const PLUGIN_ID = "signalk-aiscast";
const DEFAULT_SERVER = "https://ais.openwaters.io";
const STATUS_EVERY = 5_000;
const TOKEN_CHECK_EVERY = 6 * 3600_000; // with a token: renew a few days before expiry
const TOKEN_RETRY_MIN = 60_000; // without one (network not up yet at boot, server down): retry soon, backing off
const TOKEN_RETRY_MAX = 30 * 60_000;

export interface Config {
  share?: { targets?: boolean; ownShip?: boolean; position?: boolean };
  receive?: { mode?: ReceiveMode; radiusNm?: number };
  advanced?: { server?: string; token?: string };
  // Pre-"Advanced" layout, still honoured when read.
  server?: string;
  token?: string;
}

export default function (app: ServerAPI): Plugin {
  const events = app as unknown as EventEmitter;
  let generation = 0; // bumped by stop(); a start() still in its awaits checks it and gives up
  let teardown: (() => Promise<void>) | null = null;

  const plugin: Plugin = {
    id: PLUGIN_ID,
    name: "AIScast",
    description:
      "Show nearby traffic without an AIS receiver and share your AIS data with the world.",
    schema: {
      type: "object",
      description:
        "By enabling this plugin, you agree to the Open Waters AIS Contributor Agreement (https://github.com/openwatersio/aiscast/blob/main/docs/contributor-agreement.md): the AIS data you share is dedicated to the public domain.",
      properties: {
        share: {
          type: "object",
          title: "Share",
          properties: {
            targets: {
              type: "boolean",
              title: "Share AIS targets I receive",
              default: true,
            },
            ownShip: {
              type: "boolean",
              title: "Share my own ship's AIS transponder data",
              default: true,
            },
            position: {
              type: "boolean",
              title: "Fallback to self-reported AIS position",
              default: true,
            },
          },
        },
        receive: {
          type: "object",
          title: "Receive",
          properties: {
            mode: {
              type: "string",
              title: "Show traffic from aiscast",
              enum: ["off", "auto", "always"],
              enumNames: [
                "Off",
                "Auto (when no local receiver available)",
                "Always (fills in beyond local VHF range)",
              ],
              default: "auto",
            },
            radiusNm: {
              type: "number",
              title: "Radius around the vessel (nautical miles)",
              default: 50,
              minimum: 5,
              maximum: 200,
            },
          },
        },
        advanced: {
          type: "object",
          title: "Advanced",
          properties: {
            server: {
              type: "string",
              title: "Server",
              default: DEFAULT_SERVER,
            },
            token: {
              type: "string",
              title: "Access token (optional)",
              description:
                "Leave empty: the plugin creates a keypair on first start and requests its own personal token. Paste a token from the aiscast operator to publish with a named station and higher limits.",
            },
          },
        },
      },
    },
    // A function so the MMSI check runs fresh on every config-page load.
    uiSchema: () => ({
      "ui:order": ["share", "receive", "advanced"],
      share: {
        // the checkbox widget puts schema descriptions above the label; ui:help renders below
        targets: {
          "ui:help":
            "Other vessels visible from a connected NMEA 0183/2000 AIS receiver.",
        },
        ownShip: {
          "ui:help":
            "Forward what the AIS transponder broadcasts; it is already public on VHF.",
        },
        position: app.getSelfPath("mmsi")
          ? {
              "ui:help":
                "When an AIS transponder is not available, but GPS position is, synthesize class B AIS reports from Signal K data.",
            }
          : {
              "ui:disabled": true,
              "ui:help": "Needs an MMSI in Vessel settings.",
            },
      },
      receive: { mode: { "ui:widget": "radio" } },
      advanced: { token: { "ui:widget": "password" } },
    }),

    start(config: object) {
      const gen = ++generation;
      startAsync(config as Config, gen).catch((err) => {
        app.setPluginError(`start failed: ${(err as Error).message}`);
      });
    },

    async stop() {
      generation++;
      const t = teardown;
      teardown = null;
      await t?.();
    },
  };

  async function startAsync(config: Config, gen: number): Promise<void> {
    const server = (
      config.advanced?.server ||
      config.server ||
      DEFAULT_SERVER
    ).replace(/\/+$/, "");
    const configuredToken = config.advanced?.token || config.token;
    const wsBase = server.replace(/^http/, "ws");
    const shareTargets = config.share?.targets ?? true;
    const shareOwn = config.share?.ownShip ?? true;
    // Absent from a config saved before the setting existed: off, so an upgrade never enables it silently.
    const sharePosition = config.share?.position ?? false;
    if (sharePosition && !app.getSelfPath("mmsi")) app.debug("self-reported position is on but no MMSI is set; nothing will be synthesized");
    const mode = config.receive?.mode ?? "auto";
    const radiusNm = Math.min(200, Math.max(5, config.receive?.radiusNm ?? 50));
    const dir = app.getDataDirPath();
    const log = (msg: string) => app.debug(msg);
    const live = () => gen === generation;

    const identity = await loadIdentity(dir);
    if (!live()) return;
    let token: Token | null = null;
    let publishRefused = false;
    let errorShown = false; // the server keeps a plugin's last error on the Dashboard until the status entry is reset
    const reportError = (msg: string) => {
      errorShown = true;
      app.setPluginError(msg);
    };
    const clearError = () => {
      if (!errorShown) return;
      errorShown = false;
      app.setPluginStatus(""); // an empty status deletes the entry, lastError included
    };
    const selfSource = `v1:ed25519:${identity.pubkey}`;
    const refreshToken = async (): Promise<void> => {
      if (configuredToken) {
        token = {
          token: configuredToken,
          exp: Infinity,
          pubkey: identity.pubkey,
          server,
        };
        return;
      }
      try {
        token = await loadToken(dir, server, identity.pubkey);
        clearError();
      } catch (err) {
        token = null;
        reportError(
          `No token: ${describe(err)}. Receiving only; nothing is shared. Retrying.`,
        );
      }
    };
    await refreshToken();
    if (!live()) return;

    // Token in a header, not the URL, so it stays out of proxy and server logs.
    const l = new Link(
      () => `${wsBase}/v1/stream`,
      (): Record<string, string> =>
        token ? { Authorization: `Bearer ${token.token}` } : {},
      log,
    );
    const up = new Uplink(l, dir, log);
    const own = new OwnShip(app, (s, now) => up.hear(s, now, SOURCE_TAG));
    const down = new Downlink(
      app,
      l,
      {
        mode,
        radiusNm,
        source: `${PLUGIN_ID}.net`,
        selfSource,
        onReceived: (s) => up.noteReceived(s),
      },
      log,
    );
    const canShare = () =>
      token !== null && !publishRefused && (shareTargets || shareOwn || sharePosition);
    up.enabled = canShare();

    l.on("open", () => {
      if (publishRefused) clearError();
      publishRefused = false;
      up.enabled = canShare();
    });
    l.on("error", (message) => {
      if (/token/i.test(message) && !/publish/.test(message)) {
        reportError(`aiscast refused the token: ${message}`);
        if (!configuredToken) {
          const refused = token?.token;
          // A fresh token reconnects at once; the same token again waits out the refusal backoff.
          forgetToken(dir)
            .then(refreshToken)
            .then(() => {
              up.enabled = canShare();
              if (token && token.token !== refused) l.reconnect();
            })
            .catch((err) => log(`token refresh: ${(err as Error).message}`));
        }
      } else if (/publish/.test(message)) {
        publishRefused = true;
        up.enabled = false;
        reportError(`aiscast refused publishing: ${message}`);
      } else if (/bbox/.test(message)) {
        reportError(
          `aiscast refused the subscription: ${message} (reduce the radius)`,
        );
      } else {
        log(`aiscast: ${message}`);
      }
    });

    await up.start();
    if (!live()) {
      await up.stop();
      return;
    }
    down.start();
    l.start();
    if (sharePosition) own.start();

    const onSentence = (sentence: unknown, source?: string) => {
      if (typeof sentence !== "string" || !isAis(sentence)) return;
      const now = Date.now();
      const isOwn = isOwnShip(sentence);
      if (isOwn) own.heard(now);
      else down.localHeard(now);
      if (isOwn ? shareOwn : shareTargets) up.hear(sentence, now, source);
    };
    const onNmea = (sentence: unknown) => onSentence(sentence);
    const onN2k = (msg: unknown) => {
      const m = msg as
        | { pgn?: number; fields?: Record<string, unknown> }
        | undefined;
      if (!m || typeof m.pgn !== "number") return;
      try {
        onSentence(
          n2kToSentence(
            app,
            m as { pgn: number; fields?: Record<string, unknown> },
          ),
          "n2k",
        );
      } catch (err) {
        log(`N2K PGN ${m.pgn}: ${(err as Error).message}`);
      }
    };
    events.on("nmea0183", onNmea);
    events.on("N2KAnalyzerOut", onN2k);

    // Token check: every 6 h once we have one; 1 → 30 min backoff while we don't.
    let tokenTimer: NodeJS.Timeout | null = null;
    let tokenRetry = TOKEN_RETRY_MIN;
    const scheduleTokenCheck = (delay: number) => {
      tokenTimer = setTimeout(() => {
        const before = token?.token;
        refreshToken()
          .catch((err) => log(`token refresh: ${describe(err)}`))
          .then(() => {
            up.enabled = canShare();
            if (token?.token !== before) l.reconnect();
            if (token) {
              tokenRetry = TOKEN_RETRY_MIN;
              scheduleTokenCheck(TOKEN_CHECK_EVERY);
            } else {
              tokenRetry = Math.min(TOKEN_RETRY_MAX, tokenRetry * 2);
              scheduleTokenCheck(tokenRetry);
            }
          });
      }, delay);
    };
    scheduleTokenCheck(token ? TOKEN_CHECK_EVERY : TOKEN_RETRY_MIN);

    let lastSent = 0;
    let lastAt = Date.now();
    const timers = [
      setInterval(() => {
        const now = Date.now();
        const perMin = Math.round(
          ((up.stats.sent - lastSent) * 60_000) / (now - lastAt),
        );
        lastSent = up.stats.sent;
        lastAt = now;
        const key = identity.pubkey.slice(0, 8);
        const upText = up.enabled
          ? `↑ ${perMin}/min (queue ${up.stats.queued})`
          : "↑ off";
        const downText =
          mode === "off"
            ? "↓ off"
            : down.stats.subscribed
              ? `↓ ${down.stats.targets} targets`
              : "↓ idle";
        const linkText = l.open
          ? "connected"
          : l.lastRefusal
            ? `refused: ${l.lastRefusal}`
            : "reconnecting";
        app.setPluginStatus(`key ${key}…  ${upText}  ${downText}  ${linkText}`);
      }, STATUS_EVERY),
    ];

    teardown = async () => {
      for (const t of timers) clearInterval(t);
      if (tokenTimer) clearTimeout(tokenTimer);
      events.removeListener("nmea0183", onNmea);
      events.removeListener("N2KAnalyzerOut", onN2k);
      own.stop();
      down.stop();
      await up.stop();
      l.stop();
    };
    if (!live()) {
      const t = teardown;
      teardown = null;
      await t();
    }
  }

  return plugin;
}

// fetch's "fetch failed" hides the real reason (ECONNREFUSED, ENOTFOUND, a TLS error) in `cause`.
function describe(err: unknown): string {
  const e = err as {
    message?: string;
    cause?: { code?: string; message?: string };
  };
  const cause = e.cause?.code ?? e.cause?.message;
  return cause ? `${e.message} (${cause})` : (e.message ?? String(err));
}
