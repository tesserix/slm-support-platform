---
title: Data and privacy — what StockPilot stores
tags: [privacy, data, security]
audience: customer
---

# Data and privacy — what StockPilot stores

StockPilot reads from your broker but does not trade on your behalf. Everything here is about what we hold, where, and how to remove it.

## What we store

- **Account metadata** — your email, login provider, when you signed up.
- **Broker connection** — an OAuth refresh token per connected broker. The token has **read-only scope** at your broker's end.
- **Portfolio snapshots** — your positions, trade history, and computed P&L. We store snapshots over time so charts work.
- **Agent traces** — when StockPilot's analysis agent runs (e.g. you ask "summarise my Q3"), we store the prompt + tools-called audit trail so you can replay why a recommendation was made.

## What we don't store

- Your broker password. We never see it — OAuth flows redirect you to the broker.
- Your bank account or PAN. We don't need them.
- Card details. We use a payment processor for subscriptions; they hold the card data, not us.

## Deleting your data

From **Settings → Privacy → Delete my data** you can request a full deletion:

- Broker connections — revoked immediately.
- Portfolio snapshots — hard-deleted within 24 hours.
- Agent traces — anonymised (your user_id stripped) within 24 hours but retained in aggregate for safety auditing.
- Subscription / billing records — retained for 7 years as required by tax law.

You'll get an email confirmation when each step completes.

## I want to know what's been pulled about me

The same Privacy page has an **"Export my data"** button. We zip up everything tied to your user_id and email a download link, valid for 24 hours. Reports include a manifest listing the tables and what we read from your broker.

## I think someone else has accessed my account

If you suspect compromise, **disconnect your brokers from the Privacy page first** (this revokes the OAuth tokens) — that stops any further sync regardless of who has your StockPilot login. Then change your StockPilot password from Settings. Contact support with the **approximate timestamp of suspicious activity** for a session audit.
