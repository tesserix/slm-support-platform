---
title: Payment problems and resolutions
tags: [payment, charges, billing]
audience: customer
---

# Payment problems and resolutions

mark8ly accepts UPI, all major credit and debit cards, net banking, and store credit. Most payment failures fall into one of four buckets.

## The payment failed but my bank shows a charge

This is almost always a **deferred capture** that the bank reverses automatically. Banks place a temporary hold during the payment attempt; if mark8ly didn't capture the funds (because authentication failed, you exited, etc.), the hold drops off within 5-7 business days.

If you can see the same amount captured **twice** on your statement, file a ticket with both transaction reference numbers. We'll reconcile and refund the duplicate.

## I was charged but no order was created

Same root cause as above — the bank reported the charge before mark8ly confirmed the order. Wait 30 minutes; if the order still isn't on your account, contact support with the **transaction reference** from your bank statement. We'll either create the order manually or refund the captured amount.

## My UPI keeps timing out

UPI mandates a response within 30 seconds. Common causes:

- Your UPI app is on a different network than your phone's SIM (e.g. WiFi vs cellular). Try switching networks.
- The receiving bank is slow. Retry after a few minutes or pay with a different UPI app.

If you've retried three times, switch to a different payment method or contact support — we can issue a payment link valid for 30 minutes that bypasses the standard UPI flow.

## Refunds I expected didn't land

See the returns and refunds article for the standard refund timeline. If the refund is for a cancelled order (not a return), the timeline is the same:

- UPI / wallet: within 24 hours
- Cards: 5-7 business days
- Cash on delivery: as store credit (switchable to bank transfer)

If you're past those windows, share the **order_id** with support and we'll pull the refund audit log.
