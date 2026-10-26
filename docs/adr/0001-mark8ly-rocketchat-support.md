# Mark8ly support conversations in Rocket.Chat

Status: accepted. Runtime enablement and verification are controlled through GitOps.

The existing mark8ly merchant support widget and mobile bridge call shared Otto
under `platform/default`. Other products use the same scope, so scope alone must
never select mark8ly. The authenticated mark8ly proxies stamp `X-Product-Id` and
Otto persists this provenance when creating a conversation. Existing unmarked
conversations are not backfilled.

Planning envelope (assumptions, not measured traffic): 10 simultaneous cases,
1 message/second peak, 4 KiB text messages, approximately 1:1 read/write workload,
100 cases/day with 20 messages each. At 2 KiB average stored message size this is
about 1.5 GiB/year and 4.5 GiB over three years before indexes. Target healthy
bridge delivery p99 under 30 seconds and 99.9% monthly availability; the original
chat remains usable during Rocket.Chat outages. Validate these assumptions from
production metrics before increasing traffic tenfold.

Reuse Otto's MongoDB and worker process. Persisted conversations/messages are the
durable delivery source; deterministic Rocket.Chat IDs make retries safe. A case
has one root message and each customer message appears in its thread. Use a
separate bot subscribed only to `support-mark8ly`; store its credentials in
OpenBao and inject through ESO. No new service or disk is necessary.

For two-way operation, accept only new plain-text thread replies from configured
Rocket.Chat staff IDs in the configured room, bound to a known mark8ly case.
Ignore bot messages, top-level channel chat, edits, attachments, and other rooms.
Apply Otto's content policy and closed/assignee checks. Mongo transactions and
stable message IDs make customer-visible replies and counters idempotent.
Customers never receive bot credentials or access to the internal channel.

The assets are customer support text, identities, and credentials. Attackers may
include a different product's authenticated user, a forged webhook sender, or a
compromised bot. Product routing comes only from the authenticated server proxy;
room, thread, author, and case ownership are checked at the return boundary.
Use HTTPS, bounded responses, timeouts, no redirects, and sanitized errors.

Roll out provenance first, with delivery disabled, then the bridge binary and
Ready secret, and finally enable the worker. Verify synthetic traffic, duplicate
delivery, a rejected cross-product case, and (for two-way mode) a staff reply
returning to the original customer. Disable via GitOps for rollback and retain
stored messages for recovery. Never bulk replay historical cases.

A fire-and-forget HTTP call in the customer handler was rejected because an
outage would lose messages or block chat. Replacing the widget was rejected
because web/mobile clients already share a working Otto conversation contract.
Incremental infrastructure cost is zero; use the existing standard storage.
