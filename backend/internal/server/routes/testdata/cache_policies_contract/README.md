# Runtime cache policy contract fixtures

The 27 JSON files are byte-for-byte copies of the shared administrator panel contract.
SHA256.json records each source hash. Tests verify every hash before replaying requests.

History fixtures include _fixture metadata for all persisted successful revisions,
the stored revision/timestamp and the fixed server clock. This metadata is not an API field.
Pagination changes the returned items; completeness uses the full persisted history.

The M1 build exposes master and observability. Future milestone examples are replayed
against M1 availability; confirmation and revision errors are also tested on available keys.
Coverage includes C-1 through C-13 and the C-14 settings link value.
Golden response bodies and shared JSON examples must not be regenerated to make tests pass.
