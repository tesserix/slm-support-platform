# Application secrets

Use in-cluster OpenBao for all new and migrated Support Platform application,
provider, session, tenant and user secrets. Name owned application credentials
`support-platform-<secret>` under `support-platform/app/`; keep environment and
user/tenant boundaries explicit. Use namespace-bound least-privilege identities,
ESO for application configuration, and owner-scoped runtime access for user data.
Update writers and rotation procedures as well as readers.

GCP Secret Manager is reserved for critical shared platform/bootstrap/recovery
material. A service named `platform-mcp` is a product service, not an exception.
Shared credentials retain their owning product's paths and delegated read grants.
Never put credential values in source, output, logs or example configuration.

Changes to deployment resources go through `tesserix/tesserix-k8s`. Before source
deletion verify encrypted recovery, isolated restore, fresh reads, byte equality,
application behavior and retirement of temporary write access. Track remaining
cutover/provider acceptance in tesserix/tesserix-k8s#1209. Existing GCP references
in historical documents describe migration work, not a default for new features.
