# Changelog

All notable changes to AI Cloud Status are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project uses
[Semantic Versioning](https://semver.org/).

## [1.1.0] - 2026-10-12

### Added

- **Mistral is monitored again.** Its status page moved from Checkly to Rootly and
  exposes no API or RSS, so the page itself is now the feed: a Rootly adapter reads
  every service's status off the server-rendered HTML, folds the affected services
  into one incident titled with the page's own banner, and fails closed on anything
  it cannot read (no service rows, no banner, a truncated body) so a challenge page
  or an error page never reads as "operational".
- **Page providers are fetched as the document they are.** A provider whose feed is
  its status page's HTML is requested with the Fetch Metadata a top-level navigation
  carries, under the app's own User-Agent. That is what gets the request past the
  Cloudflare managed challenge in front of status.mistral.ai; nothing claims to be
  a browser.
- **A Cloudflare challenge is named as one.** A feed that answers with a Cloudflare
  challenge is reported in the row as such, in all ten languages, instead of as
  "HTTP 200" or "HTTP 403"; the log says `reason=challenged`. On an optional
  provider the challenge is retried on a fresh connection and, if it persists,
  filed as "no public feed" rather than as an outage.
- **The capture archive stays readable across a feed change.** An archived capture
  recorded under a provider's previous feed is read by the adapter of its time, so
  the alert audit no longer reports a changed feed as a broken parser.

### Changed

- A page provider's capture is archived as `.html.gz`; feed captures keep the
  `.json.gz` name. The archive's readers key on the timestamp and the gzip magic,
  never on the suffix.
- The capture archive deduplicates a page by what the parser read from it, so an
  open incident on a page that mints fresh tokens on every response is archived once
  per state, not once per poll.
- The coverage record is keyed on the polling round rather than on the wall clock:
  one failure per round, a manual retry filed exactly like a scheduled poll, a stale
  retry counted as an attempt only, and a poll through a dead link counted as blind
  rather than as the provider's fault.
- Quit drains the polls in flight before the shutdown save, and the main menu's
  Quit on macOS runs the same shutdown save as the tray's.
- An unreadable `history.json` degrades the alert audit's two sections instead of
  aborting the run; only a corrupt file is set aside, an intact-but-unreadable one is
  left in place and the run simply does not save.

### Fixed

- The replay tests over the real capture archive read every capture raw while the
  archive is gzipped, so they proved nothing; they decompress on the gzip magic now,
  label compound extensions correctly, include the "none" captures that list
  incidents, and report an unreadable file instead of skipping it in silence.
- A failure class with no coverage outcome can no longer fall through to
  "transport" unnoticed: the outcome table is held to the classifier's own bound.

## [1.0.0] - 2026-08-15

First release: a tray app that polls the public status feeds of AI and cloud
providers, with per-region incident scoping, custom URL checks, an offline
detector, desktop notifications, a feed-capture archive and an alert audit tool.
