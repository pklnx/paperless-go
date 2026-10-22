-- +goose Up
-- Caps on open passkey logins per network (#48), on top of the cap per
-- client (client_key: the IPv4 address or the IPv6 /64) and the total.
-- The names are neutral so both families fit:
--   client_net_mid  the IPv6 /56 or the IPv4 /24,
--   client_net_wide the IPv6 /48; NULL for IPv4.
-- Both are NULL when the client has no address (and for registrations).
-- No backfill: open logins expire within 5 minutes, and until then rows
-- without the columns count only towards the client cap and the total.
ALTER TABLE webauthn_ceremonies
    ADD COLUMN client_net_mid  TEXT,
    ADD COLUMN client_net_wide TEXT;

-- +goose Down
-- Without the columns only the client cap and the total apply.
ALTER TABLE webauthn_ceremonies
    DROP COLUMN client_net_wide,
    DROP COLUMN client_net_mid;
