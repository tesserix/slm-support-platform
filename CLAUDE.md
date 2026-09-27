# Support Platform working rules

Follow [AGENTS.md](AGENTS.md). OpenBao is the default for application, provider,
session and user/tenant credentials; use `support-platform-<secret>` identifiers.
Only critical shared platform/bootstrap/recovery secrets belong in GCP Secret
Manager. Keep other products' shared credentials under their own OpenBao paths.

Infrastructure policy and rollout are owned by `tesserix/tesserix-k8s`; see its
`docs/application-secret-policy.md` and migration issue #1209. Do not create new
GCP application secrets or restore legacy GCP writers when adding features.
