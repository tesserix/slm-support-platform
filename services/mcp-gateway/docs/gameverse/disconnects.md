---
title: Disconnects and abandoned matches
tags: [disconnect, abandon, rating]
audience: customer
---

# Disconnects and abandoned matches

GameVerse tries hard to distinguish a **genuine disconnect** (your network dropped) from a **rage-quit** (you closed the app on purpose). The difference matters because we don't punish the former but do penalise the latter.

## How we tell them apart

- **Genuine disconnect** — the server stops receiving heartbeats from your client but received them up to a few seconds before. Reconnects within the grace window (60 seconds for most games) resume the match. Frequent reconnects from the same session are tracked but not penalised on a single occurrence.
- **Rage-quit** — the client signals a deliberate close (the in-app "Concede" / "Leave match" flow), or the client closes cleanly with no preceding network instability.

If you genuinely had a bad network and you got penalised, raise a ticket with the **match_id** and the approximate **disconnect timestamp** — we audit the heartbeat log and reverse the rating penalty if it was misclassified.

## What happens during the grace window

When your client drops out, we hold the match open for the grace window. Other players see a "Reconnecting…" banner. If you come back the match continues exactly where it was. If you don't:

- **Casual games** — match is abandoned. Neither player's rating changes.
- **Ranked games** — match is decided as a loss for the disconnected player. Rating moves.

## Repeated disconnects

If we see your account drop ≥3 ranked games in a 24-hour window, we put you on a 1-hour ranked-queue cooldown. Casual queue is still available. This is to protect other players' game experience — not a punishment. The cooldown lifts automatically; no ticket needed.

If you're seeing repeated disconnects despite a stable network, your ISP may be doing aggressive idle-timeout on the WebSocket — try toggling between cellular and WiFi to confirm. Report it with traceroute output if you can.
