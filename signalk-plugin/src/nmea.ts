// Sentence-level helpers. The plugin never decodes AIS itself; aiscast and the server's parser do that.

const TAG_BLOCK = /^\\[^\\]*\\/;
// Optional TAG block, then !AIVDM / !BSVDM / !AIVDO ... from any talker.
const AIS_SENTENCE = /^(\\[^\\]*\\)?[!$][A-Z]{2}VD[MO],/;

export function isAis(sentence: string): boolean {
  return AIS_SENTENCE.test(sentence);
}

export function isOwnShip(sentence: string): boolean {
  return /^(\\[^\\]*\\)?[!$][A-Z]{2}VDO,/.test(sentence);
}

export function stripTag(sentence: string): string {
  return sentence.replace(TAG_BLOCK, "");
}

export function xorChecksum(s: string): string {
  let sum = 0;
  for (let i = 0; i < s.length; i++) sum ^= s.charCodeAt(i);
  return sum.toString(16).toUpperCase().padStart(2, "0");
}

// Prepend a NMEA 4.10 TAG block carrying the receive time (and a source label such as `n2k` for re-encoded
// sentences), unless the sentence already has one.
export function tagged(sentence: string, receivedAt: number, source?: string): string {
  if (TAG_BLOCK.test(sentence)) return sentence;
  const tag = `${source ? `s:${source},` : ""}c:${receivedAt}`; // 13 digits: aiscast reads that as milliseconds
  return `\\${tag}*${xorChecksum(tag)}\\${sentence}`;
}

// The armoured payload (6th field) plus channel, which is what aiscast dedupes on.
export function payloadKey(sentence: string): string {
  const f = stripTag(sentence).split(",");
  return f.length > 5 ? `${f[5]}/${f[4]}` : sentence;
}
