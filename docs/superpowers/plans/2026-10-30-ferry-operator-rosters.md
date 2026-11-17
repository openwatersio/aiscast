# Ferry operator rosters implementation plan

> Execute inline in the existing ferry-identities worktree. The approved design is the operator-roster proposal in this conversation.

**Goal:** Identify BC Ferries, Black Ball and Hy-Line ferries from maintained operator rosters, sharing those rosters with fleet pages.

**Architecture:** Store ferry fleets as JSON beside the existing YAML fleets. The browser imports those JSON files directly; the server parses the same files for fleet pages. Official company rosters determine membership, while confirmed AIS names, MMSIs and IMOs bind that membership to vessel records. Explicit registry ferry types remain a fallback.

**Constraints:** No dependencies, schedules, arrival predictions or inference from an unverified operator string. Keep existing vessel URLs. Refreshes produce drafts and never replace the maintained input file. Optional route descriptions remain per vessel.

## Tasks

- [x] Extend fleet loading and validation to JSON, a reviewed date, AIS-name aliases and optional service descriptions. Add failing coverage for JSON fleet membership and preserve all existing fleet behavior.
- [x] Add a refresh command with parsers for the three official sources. Check a complete BC Ferries dropdown, distinguish Hy-Line ferries from fishing/sightseeing vessels, reject empty/invalid source responses, preserve annotations, and report membership and identity changes. Exercise the parsers and draft logic with small fixtures and fake HTTP responses.
- [x] Draft and verify the full BC Ferries roster and the Black Ball and Hy-Line ferry rosters. Keep every official member, including any that cannot yet be matched to AIS, and report unresolved identities. Record official vessel links and confirmed aliases instead of guessing radio identities.
- [x] Replace the hard-coded MMSI table with shared roster lookup. Test Coastal Celebration, Coho, Grey Lady IV, missing names, conflicting IMOs, radio changes, and generic passenger ships. A confirmed IMO may follow a vessel to a new MMSI; a conflicting reported IMO must not inherit another vessel's identity.
- [x] Document refresh/review commands and the data contract, run client/workspace checks and browser verification, review the branch, then update PR #311.

## Review focus

- Complete official membership, including records beyond the first page and non-ASCII names.
- Failed or changed source HTML must never prune the maintained roster.
- Names shared by unrelated vessels must not acquire a company identity.
- JSON and YAML fleets must coexist without putting the YAML parser in the browser.
- Removed fleet members lose curated operator context; ferry category and route assignment remain separate.

## Decisions

- JSON supplies the shared data without introducing a browser YAML parser or a generation step at build time.
- Fleet titles are operator names; the source entry with that name is the official roster URL. Individual vessel links can identify a more specific official page.
- Refresh drafts are reviewed manually before publication. This task creates no recurring automation.

## Verification

All 39 BC Ferries identities were checked against live AIS and registry records on 2026-10-30. Hy-Line has four current ferries; fishing and sightseeing vessels are excluded. The live BC Ferries refresh returned no membership or identity changes. Browser inspection confirmed Coastal Celebration and the 39-vessel fleet page. The workspace tests, typecheck, styles and production build passed; the targeted Playwright vessel-page test passed after switching to direct JSON imports with import attributes.

The user approved external review of the expanded roster and Slackwater changes, including follow-up fixes, after automatic approval review requested a scope-specific confirmation.

## Slackwater follow-up

The user supplied the design for a ferry-only Slackwater card above Particulars, with an external link and aggregate click tracking. The card uses existing Tile and theme tokens. A native click covers mouse and keyboard activation; auxclick covers the middle button. Plausible uses script.tagged-events.js with a loading queue and a direct JavaScript event call, preserving automatic pageviews and avoiding tagged-link URL properties. The user explicitly allowed normal Plausible page URL/referrer context; custom props remain exactly context=ferry. All 453 workspace tests and 43 desktop/phone browser tests pass, including the early-click queue, a 390px layout, and rejection of stale fleet identities. Typecheck, styles and production build pass.

## Expansion review

The first Opus review completed its system map. Sonnet supplied findings but omitted the server writers, so its limited attempt is being retried. The PR description now covers the full change. Review fixes share the ferry identity predicate with fleet report displays and filter operator cards against the verified identity. Tests cover an unknown IMO, the early analytics queue and mismatched fleet reports. Loader diagnostics and privacy wording were clarified. The conservative IMO veto remains intentional. The roster files total 15,241 bytes raw and 2,486 bytes gzipped. Fleet batching remains deferred; an existing 72-vessel motor-yacht fleet already uses the same path. Two Opus static reviews completed. Both Sonnet attempts provided useful findings but omitted the server-writer map and are recorded as limited attempts. Final low-priority follow-ups passed the stream IMO into titles, displayed available license identifiers, documented uncounted browser-menu openings, and preserved filtered Vitest arguments. All 453 workspace tests, 43 browser tests, typecheck, styles and the production build passed. The PR description now covers the full implementation and review limits.
