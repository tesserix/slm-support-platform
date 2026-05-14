---
title: How matchmaking works
tags: [matchmaking, rating, queue]
audience: customer
---

# How matchmaking works

GameVerse matches you against players with a similar **skill rating**. The rating uses a Glicko-2 variant: every game adjusts your rating up or down based on the result, the opponent's rating, and how surprising the outcome was.

## Rating bands

- **Newcomer** (rating <1000) — quick, forgiving queues. Lots of players, fast match.
- **Intermediate** (1000-1500) — most players sit here. Matches usually find an opponent in under a minute.
- **Advanced** (1500-1800) — narrower pool, queue can be 1-3 minutes.
- **Expert** (1800+) — smallest pool. Off-peak queues can take 5+ minutes; we expand the rating window after the first 2 minutes to keep things moving.

## Why was I matched against a much higher-rated opponent?

Two reasons this can happen:

- **The queue expanded** — when you've been waiting long enough that no closer-rated opponent is available, the queue widens its search to keep you from waiting forever. Your eventual opponent will still be in your broader skill band, just not your tightest.
- **A new account in the same band** — newcomers play with provisional ratings that move fast. If you're matched against someone whose rating looks suspiciously low for their play, file a ticket — we audit for smurfing.

## Why didn't my rating change after a match?

Three possibilities:

- **The game was abandoned** — if either player disconnected before the result-determination point, the rating doesn't move (we don't want one-sided ratings on abandoned games).
- **The rating system is in a stable period** — Glicko-2 includes a "deviation" parameter that tightens as you play more games. Stable players have smaller swings per game; that's a feature, not a bug.
- **There's a backfill in progress** — after a busy weekend the rating recalculator can be a few hours behind. Ratings always settle by EOD UTC.
