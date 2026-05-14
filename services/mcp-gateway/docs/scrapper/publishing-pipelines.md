---
title: Configuring publishing pipelines
tags: [publish, destination, webhook]
audience: customer
---

# Configuring publishing pipelines

A **publishing pipeline** is what happens after a scrape job collects items. Each pipeline takes the items from a job and writes them somewhere.

## Supported destinations

- **Google Sheets** — append rows. We OAuth into your Drive once; subsequent writes go to a specific sheet you pick.
- **S3** (or any S3-compatible bucket) — newline-delimited JSON. One file per run.
- **Webhook** (POST) — we batch up to 100 items per request. Your endpoint must return 2xx within 10 seconds.
- **Postgres** — write to a table you specify. We assume the schema matches the item shape; if not, we surface the mismatch as a publish error.

## Multi-destination jobs

A single scrape job can publish to multiple pipelines simultaneously. Each pipeline runs independently — a webhook failure won't stop a sheet update from succeeding. The job detail page shows per-pipeline status.

## My webhook isn't being called

Common causes:

- **Endpoint returns non-2xx** — we retry 3 times with exponential backoff. After three failures we mark the pipeline as "failing" and pause it. Resume from the pipeline detail page once you've fixed the endpoint.
- **Authentication failed** — if your webhook expects a bearer token, you can set it under the pipeline's **Auth** tab. We do *not* store this in logs or surface it in the UI after save.
- **Wrong URL** — typos. Test the URL with a manual "Send sample" from the pipeline page; we POST one synthetic item and show you the response.

## My Google Sheet shows duplicate rows after a re-run

By default we append every time the job runs. To deduplicate:

- Configure a **dedup key** in the pipeline settings. We hash the chosen field(s) per item and skip on re-run.
- Or set the pipeline to **replace** mode — every run truncates the sheet first. Use this carefully; if the scrape job fails partway, you'll lose data you had.

## Reordering pipelines

The order pipelines appear on the job page is the order they run after each scrape. You can drag-reorder from the pipeline list. Pipelines do not pass data to one another — they each receive the same item batch from the job.
