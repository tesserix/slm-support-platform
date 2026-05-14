---
title: Supported scraping platforms and their quirks
tags: [platforms, support, sources]
audience: customer
---

# Supported scraping platforms and their quirks

Each platform we scrape has its own rate limits, authentication style, and failure modes. Knowing these saves you debugging time.

## Platform list

- **Twitter / X** — requires you to attach an authenticated session. Rate-limited to ~50 requests per 15-minute window per session. Tweet content is fine; profile media is unreliable. New X anti-bot patterns occasionally break us — we usually adapt within 24 hours.
- **Instagram** — public profiles and hashtags. Stories and DMs are out of scope. Heavy rate limits; expect 1-2 requests per second.
- **Reddit** — JSON endpoint, light rate limits. Anonymous reads work; subscriber-gated subs need an authenticated session.
- **LinkedIn** — limited support. Public posts only, no scraping of profile pages (terms-of-service constraint).
- **TikTok** — public videos with the standard metadata. Audio and full-resolution downloads are out of scope.
- **YouTube** — captions, descriptions, comments. Video downloads use the official API.
- **Generic HTML** — any public URL. You configure CSS or XPath selectors. Stability depends entirely on the target.

## Platform-specific limits

The free tier has a 1,000-items-per-day limit across all sources combined. Paid tiers raise this per-platform; see **Settings → Plan**. Per-platform soft limits we recommend even on paid tiers:

- Twitter / X: <5,000 items/day per session
- Instagram: <2,000 items/day per source
- TikTok: <10,000 items/day
- Generic HTML: limited only by your target's TOS / robots.txt

## Authentication tokens

For platforms that require authentication, we store a refresh token (where available) and rotate it automatically. If the source page shows a red "Re-authenticate" banner, the token has expired or been revoked — reconnect from the source detail.

We never store passwords. The OAuth or session-cookie capture happens in your browser and the credential is encrypted at rest server-side. You can revoke from **Settings → Connected sources** at any time.

## When platforms change layouts

Layout changes can break selectors overnight. Our system:

- Detects "0 items extracted from a non-empty response" within an hour.
- Triggers a re-parse attempt with the previous version's selectors.
- If that still fails, flags the source for human review and emails you.

You can opt into alerts under **Settings → Notifications**. Default is one summary email per platform per day.
