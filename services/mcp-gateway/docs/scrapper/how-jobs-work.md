---
title: How scrape jobs work
tags: [jobs, scraping, pipeline]
audience: customer
---

# How scrape jobs work

A **scrape job** runs your configured scrape against the target platform on a schedule. It moves through a pipeline of stages, each independently retryable.

## Lifecycle of a job

1. **Queued** — accepted by the planner; waiting for capacity.
2. **Fetching** — the target platform is being hit. Most platforms allow our crawler 1-3 requests per second; jobs can spend a few minutes here for any target with more than ~100 items.
3. **Parsing** — raw responses are being structured. Fast unless the platform changed its layout (in which case we retry with the previous parser and flag the job).
4. **Enriching** — optional NLP / classification / sentiment passes if you enabled them.
5. **Publishing** — pushing the cleaned data to your destination (sheet, S3, webhook, etc.).
6. **Done** — terminal state. Re-runs in your next scheduled window.

You can see live progress on the job detail page. Each stage shows wall-clock time and any partial-failure counts.

## My job is stuck in "Fetching"

If the stage shows progress (item count climbing), it's just a large scrape and you're seeing rate-limiting at work. ETA is shown when we have enough data to estimate.

If item count is **flat for >5 minutes**:

- The target platform may have rate-limited us harder than usual. The job will retry; if it fails three retries in a row it surfaces as a "blocked" status with the actual response.
- The platform may have changed authentication. Reconnect from the job's **Source** tab.

## Quota limits

Free-tier accounts: 1,000 items per day per source, max 3 sources, max 10 scheduled jobs.

Paid tiers raise these significantly. See the pricing page or **Settings → Plan** for your current usage and limits.

If you hit a quota mid-run, the job pauses and shows a yellow banner. Existing items continue to publish; new items wait until the next quota window resets (UTC midnight for daily, UTC Sunday for weekly).

## Why was my job auto-disabled?

We disable a job that has failed at the **fetching** stage **5+ times in a row** because that's almost always a credential / target-config issue we can't fix from our end. The disable email lists which check failed. Fix the credential or target URL, then re-enable from the job page.
