import { readFile } from "node:fs/promises";

export const BUDDYLIST_OPTIONS = "signalk-buddylist-plugin.json";

// The buddy list belongs to signalk-buddylist-plugin (Freeboard-SK's "Is Buddy" button writes to it, and it
// raises the vessels.<urn>.buddy flag and proximity notifications once positions arrive). This watches its
// saved options for MMSIs to follow on aiscast. No file, plugin disabled, or no buddies all mean an empty list.
export class Buddies {
  private timer: NodeJS.Timeout | null = null;
  private stopped = false;
  private last = ""; // matches the empty list, so a missing file never reports a change
  mmsis: number[] = [];

  constructor(
    private readonly file: string, // <config>/plugin-config-data/signalk-buddylist-plugin.json
    private readonly selfMmsi: string | null,
    private readonly onChange: (mmsis: number[]) => void,
    private readonly pollMs = 60_000, // ponytail: polling, fs.watch if a minute ever feels slow
  ) {}

  start(): void {
    this.stop();
    this.stopped = false;
    void this.poll();
    this.timer = setInterval(() => void this.poll(), this.pollMs);
  }

  stop(): void {
    this.stopped = true;
    if (this.timer) clearInterval(this.timer);
    this.timer = null;
  }

  private async poll(): Promise<void> {
    let mmsis: number[] = [];
    try {
      const opts = JSON.parse(await readFile(this.file, "utf8")) as {
        enabled?: boolean;
        configuration?: { buddies?: { urn?: string }[] };
      };
      if (opts.enabled !== false) {
        mmsis = (opts.configuration?.buddies ?? [])
          // only real MMSIs reach the subscription; a malformed one would refuse the whole frame
          .map((b) => /^urn:mrn:imo:mmsi:(\d{9})$/.exec(b.urn ?? "")?.[1])
          .filter((m): m is string => m != null && m !== this.selfMmsi)
          .map(Number);
      }
    } catch (err) {
      // Missing file means the buddylist plugin is not installed or never configured: an empty list.
      // Anything else (EACCES, EMFILE, a torn read) is transient: keep the last list and try again later.
      if ((err as NodeJS.ErrnoException).code !== "ENOENT") return;
    }
    mmsis = [...new Set(mmsis)].sort((a, b) => a - b);
    const key = mmsis.join();
    if (this.stopped || key === this.last) return;
    this.last = key;
    this.mmsis = mmsis;
    this.onChange(mmsis);
  }
}
