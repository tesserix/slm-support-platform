---
title: Account recovery and login issues
tags: [login, recovery, account]
audience: customer
---

# Account recovery and login issues

GameVerse uses passwordless login via your email (a magic-link sent on every sign-in). If you can't get in, one of these is happening.

## I'm not receiving the magic link

- **Check spam / promotions tabs** — providers occasionally misclassify the email.
- **The address is wrong** — typos at signup are the most common cause. The magic link went to whatever address you signed up with, not necessarily the one you typed today.
- **Email throttled** — we send at most one magic link per address per minute. Wait 60 seconds and try again.

If none of these work, raise a ticket with the **email you signed up with** and an approximate signup date. We can manually issue a one-time recovery link.

## My account was hacked / used by someone else

Three things to do, in order:

1. **Sign out everywhere** from Settings → Devices. This invalidates every active session.
2. **Disconnect linked services** (any tournament platform, friends list integration) so the attacker can't repeat the path.
3. **Raise a ticket** with the timestamp you first noticed the issue. We audit access logs and roll back any rating changes from the breach window.

## I can sign in but my profile looks empty

This is almost always a **regional account mismatch** — GameVerse has separate accounts per region (India / SEA / EU) and they don't auto-merge. If you've signed up under two regions there are literally two accounts.

You can merge them from **Settings → Account → Merge regional accounts**. The merge is irreversible and combines rating history (weighted average), game history (concatenated), and friends (union). Process takes up to an hour to complete.

## I want to delete my account

From **Settings → Privacy → Delete account**. Confirmation is required and there's a 7-day cooling-off period before the deletion processes — you can cancel from any device within that window. Ratings, games, friends, and personal data are fully removed. Anonymised match logs are kept for fairness auditing.
