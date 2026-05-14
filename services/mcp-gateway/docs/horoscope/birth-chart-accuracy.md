---
title: How birth chart accuracy works
tags: [birth, chart, accuracy, time]
audience: customer
---

# How birth chart accuracy works

A birth chart's accuracy depends on three inputs: **date**, **time**, and **place** of birth. Date and place are usually exact; time is where most users see drift.

## Why time matters so much

The ascendant and house cusps move roughly **1° every 4 minutes**. A 30-minute error in birth time can shift your ascendant by a full sign — which then propagates to every house-based interpretation. The Sun sign barely moves (a degree or two), so sun-sign-only readings are tolerant of time errors; full natal interpretations are not.

If you don't know your exact birth time:

- **Within 2 hours** — readings are reliable for planetary signs (Sun, Moon, Mercury, etc.) but houses may be off by 1 sign. Avoid hour-by-hour timing predictions ("muhurta" / "transit timing").
- **Within 30 minutes** — most modern readings work well except for sub-house cusp interpretations.
- **Exact (within minutes)** — every layer of the chart is meaningful, including divisional charts (D-9 / Navamsa, D-10, etc.).

## I have my birth certificate; what time format should I enter?

Use the hospital-recorded time, in **local time** (the time of day where you were born, not converted to anything). Tap the calendar field, fill in date, then the time picker. We handle DST and timezone conversion using the place-of-birth field — you don't need to do any math.

## My chart looks different from another astrology app

Two common causes:

- **Ayanamsa difference** — Vedic charts shift by a fixed offset (the ayanamsa) to account for the precession of the equinoxes. Different schools (Lahiri, Raman, Krishnamurti) use slightly different values. We default to **Lahiri**, the most-used in modern Vedic astrology. You can switch from Settings.
- **House system** — Western charts use Placidus, Koch, or Equal house systems. We default to **Whole Sign** (the Vedic standard) but expose Placidus and others under chart settings for cross-checking with Western tools.

If your chart still doesn't match across two tools with the same ayanamsa and house system, the most likely cause is a timezone bug somewhere — double-check that both tools agree on the timezone for your place of birth, especially for older dates where India shifted from IST 5:30 to IST 6:00 historically.

## Updating a birth time

You can edit birth time once per account. Use **Settings → Birth details**. Editing recomputes every chart and reading from the new time — historical readings get an "updated" badge so you can see the change.
