import { Check, Ellipsis, Share } from "lucide-react";
import { useState } from "react";
import { Link } from "react-router";
import { CONTRIBUTE, GITHUB } from "../lib/links";
import { SITE } from "../lib/nav";
import { shareLink } from "../lib/share";
import { ThemeButton, ThemeMenuItem } from "./ThemeToggle";
import { IconButton, iconButtonClass } from "./ui/IconButton";
import { GitHubMark, Logo } from "./ui/Logo";
import { Menu, MenuLinkItem } from "./ui/Menu";

/**
 * The bar across the top, as on the website: the name on the left, and on the right the
 * project around the map, which all lives on the website. Browsing the data is the panel's
 * job. What the bar has no room for folds into a menu that lists it the same way.
 */
export function Header() {
  return (
    // Glass over the map like the panel, which the camera keeps what it frames out from under.
    // Stacked under the panel and the chips, so its shadow falls behind them.
    <header data-map-inset-top className="site-header pane fixed z-5 flex items-center gap-2 px-3 md:px-8">
      <Link to="/" className="flex min-w-0 items-center gap-2 text-fg no-underline hover:text-fg">
        <Logo className="size-6" />
        <span className="truncate text-headline font-semibold">Open Waters AIS</span>
      </Link>

      <nav aria-label="Site" className="ml-auto hidden items-center gap-5 lg:flex">
        {SITE.map((l) => (
          <a key={l.label} href={l.href} className="text-subhead font-medium text-fg-secondary no-underline hover:text-fg">
            {l.label}
          </a>
        ))}
      </nav>

      <div className="ml-auto flex items-center gap-1 lg:ml-3">
        <a
          href={CONTRIBUTE}
          className="mr-1 hidden rounded-full bg-accent px-3.5 py-1.5 text-subhead font-semibold text-surface no-underline hover:bg-accent-hover hover:text-surface sm:inline-block"
        >
          Contribute
        </a>
        <ShareView />
        <ThemeButton />
        <a href={GITHUB} aria-label="Source code on GitHub" title="Source code on GitHub" className={iconButtonClass(false, "max-lg:hidden")}>
          <GitHubMark />
        </a>
        <SiteMenu />
      </div>
    </header>
  );
}

/**
 * The address as it stands, which carries the map's position in its hash, so whoever opens it
 * sees what the sharer saw, open vessel and all.
 */
function ShareView() {
  const [copied, setCopied] = useState(false);
  async function share() {
    const done = await shareLink({ title: document.title, url: window.location.href });
    if (done !== "copied") return;
    setCopied(true);
    setTimeout(() => setCopied(false), 1500);
  }
  return <IconButton icon={copied ? Check : Share} label={copied ? "Link copied" : "Share this view"} onClick={() => void share()} />;
}

/**
 * What the bar has no room for, in the order the bar shows it: the links below a wide
 * screen, the Contribute button and the theme below the widths that fit them.
 */
function SiteMenu() {
  return (
    <div className="lg:hidden">
      <Menu
        align="end"
        trigger={
          <button type="button" aria-label="More" title="More" className={iconButtonClass()}>
            <Ellipsis className="size-5" aria-hidden />
          </button>
        }
      >
        {SITE.map((l) => (
          <MenuLinkItem key={l.label} href={l.href}>
            {l.label}
          </MenuLinkItem>
        ))}
        <MenuLinkItem href={CONTRIBUTE} className="font-semibold text-accent sm:hidden">
          Contribute
        </MenuLinkItem>
        <div className="mt-1 flex items-center justify-between border-t border-line pt-1">
          <MenuLinkItem href={GITHUB} label="Source code on GitHub" className="w-auto">
            <GitHubMark className="size-4 text-fg-secondary" />
          </MenuLinkItem>
          <ThemeMenuItem className="w-auto md:hidden" />
        </div>
      </Menu>
    </div>
  );
}
