---
title: Why is my portfolio out of date?
tags: [portfolio, sync, latency]
audience: customer
---

# Why is my portfolio out of date?

StockPilot's portfolio view is **near-live** but not strictly real-time. Different brokers update at different speeds, and we batch-poll rather than streaming, so a small lag is by design.

## Expected freshness by broker

- **Zerodha (Kite)** — positions update within 1-2 minutes during market hours.
- **Upstox** — 2-5 minutes during market hours.
- **Saxo Bank** — end-of-day for retail accounts. Intraday for premium.
- **Groww** — 5-10 minutes; Groww caches more aggressively at their end.
- **ICICI Direct** — 5-15 minutes.
- **Manual CSV** — only as fresh as your last upload.

If you've sold a position and the StockPilot home still shows you holding it after the expected window, the most likely cause is the broker hasn't published the updated position yet — refresh your broker's app first to confirm, then check StockPilot.

## My P&L numbers look wrong

P&L is computed from the cost basis the broker reports plus the current LTP (last traded price). Two common gotchas:

- **Corporate actions** (splits, bonuses, mergers) — we apply these on EOD reconciliation, so during market hours the day after a corporate action your P&L may temporarily look off. Fixed by 6pm IST.
- **Average price differs from broker** — some brokers use FIFO, others use weighted average. StockPilot inherits whatever your broker reports. If you see a mismatch with what *another* tool shows, the difference is almost always in the cost-basis methodology.

## I need full historical accuracy

For tax filings and similar, pull the data directly from your broker — they're the system of record. StockPilot's history can have small reconciliation gaps because we depend on the broker's API being responsive on every poll, and we don't claim audit-grade fidelity.
