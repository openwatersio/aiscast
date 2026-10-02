/**
 * Hands a link to the device's share sheet, or copies it where there is none. Resolves to what
 * happened, so the button can say "Copied". Dismissing the sheet is the reader's choice and
 * does nothing; anything else, such as a browser that has the API but will not open a sheet,
 * falls back to copying.
 */
export async function shareLink(link: { title: string; text?: string; url: string }): Promise<"shared" | "copied" | "dismissed"> {
  if (navigator.share) {
    try {
      await navigator.share(link);
      return "shared";
    } catch (e) {
      if (e instanceof DOMException && e.name === "AbortError") return "dismissed";
    }
  }
  await navigator.clipboard.writeText(link.url);
  return "copied";
}
