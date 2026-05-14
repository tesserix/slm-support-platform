---
title: Connecting your broker to StockPilot
tags: [broker, setup, oauth]
audience: customer
---

# Connecting your broker to StockPilot

StockPilot syncs your portfolio by connecting to your broker's read-only API. We never store your trading password — we only ever hold short-lived OAuth tokens that we refresh in the background.

## Supported brokers

- Zerodha (Kite)
- Upstox
- Saxo Bank
- Groww (read-only)
- ICICI Direct (limited)

If your broker isn't on the list, you can still use StockPilot in **manual entry mode** — upload a CSV export of your positions.

## How to connect

1. From the StockPilot home page, click **"Add account"**.
2. Pick your broker. We redirect you to your broker's login page (this is the real one — verify the URL bar shows the broker's domain, not stockpilot.com).
3. Authenticate with the broker. Grant **read-only access** (StockPilot won't ask for trade-execution scopes).
4. The broker redirects back. We confirm the connection with a heartbeat call and your portfolio appears within 30 seconds.

## My portfolio isn't syncing

A few common causes:

- **Token expired** — broker tokens are short-lived. We auto-refresh, but if you've revoked the connection in your broker's app, you'll see a "Reconnect" banner on the StockPilot home. Tap it and re-auth.
- **Broker rate-limited us** — heavy users sometimes get throttled. We back off and retry; the sync should resume within an hour without intervention.
- **Position data is delayed** — some brokers don't update positions in real time. Intraday trades may take 15-30 minutes to appear. End-of-day reconciliation always lands by 6pm IST.

If neither of those explains a >2 hour delay, raise a ticket with the **broker name** and approximate **trade time** so support can audit the sync log.

## Disconnecting

You can disconnect any time from **Settings → Connected accounts**. StockPilot keeps your historical data on file but stops pulling new positions. You can fully delete your data from the same page — see the data-handling article for what gets deleted vs. kept for legal reasons.
