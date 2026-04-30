# Prism production deployment (shared host services)

This directory is for deploying prism on a host that already runs
`agents-pay-service` (or any other service that owns the host's
Postgres + nginx). Compared to `deploy/ha-prototype/`:

- **Postgres**: not bundled — uses the existing instance, just adds
  a dedicated `prism` user + database.
- **nginx**: not bundled — host's system nginx terminates TLS and
  reverse-proxies to prism replicas via published loopback ports.
- **prism replicas**: still 3 replicas in docker-compose, sharing
  Postgres + a docker volume for the admin macaroon.

```
client → 443  ──→ host nginx (TLS, Host-based routing)
                  ├─ agents-pay.loka.cash         → existing upstream
                  ├─ prism.loka.cash              → 127.0.0.1:8081/82/83
                  └─ *.prism.loka.cash            → 127.0.0.1:8081/82/83
                                                       │
                                                       ▼
                                                  ┌─────────────┐
                                                  │ docker:     │
                                                  │  prism-1    │── shared Postgres ──┐
                                                  │  prism-2    │      127.0.0.1:5432│
                                                  │  prism-3    │                     │
                                                  └─────────────┘                     │
                                                                                      │
                                              host's existing Postgres ◄──────────────┘
                                              (also serves agents-pay)
```

## One-time host setup

### 1. Postgres — create user + database (one-time, admin work)

This step only creates the empty database and a Postgres user that
can log into it. **Tables are created by prism itself on first
boot** — it runs schema migrations from `aperturedb/sqlc/migrations/`
via golang-migrate. Don't try to apply DDL by hand.

The reason you do this manually: Postgres won't let a non-privileged
client run `CREATE DATABASE` / `CREATE USER`, so prism's own
`prism` user can't bootstrap itself. Giving prism a superuser
account would be a security anti-pattern (compromise of one
service would expose the whole instance, including agents-pay's
database).

**Option A (recommended): use the bootstrap script.** Idempotent —
re-running just resets the password.

```bash
PRISM_DB_PASSWORD='<strong-password>' sudo -E ./bootstrap.sh
# or, omit env to be prompted:
sudo ./bootstrap.sh
```

**Option B: do it by hand.** What the script runs, expanded:

```bash
sudo -u postgres psql
```

```sql
CREATE USER prism WITH PASSWORD '<strong-password>';
CREATE DATABASE prism OWNER prism ENCODING 'UTF8';
\c prism
GRANT ALL ON SCHEMA public TO prism;
\q
```

After prism's first start you can confirm migrations ran:

```bash
PGPASSWORD='<password>' psql -h 127.0.0.1 -U prism -d prism -c '\dt'
# Should list: secrets, services, l402_transactions, mpp_sessions,
# schema_migrations, onion, lnc_sessions
```

### 2. Postgres — accept connections from docker bridge

Edit `/etc/postgresql/15/main/pg_hba.conf` (path varies by distro/version):

```
# allow prism container to connect to its own DB
host    prism    prism    172.16.0.0/12     md5    # docker default bridge
host    prism    prism    192.168.0.0/16    md5    # docker custom networks
```

If your `postgresql.conf` has `listen_addresses = 'localhost'`, change to:

```
listen_addresses = 'localhost, 172.17.0.1'   # or '*' if you trust pg_hba
```

Reload: `sudo systemctl reload postgresql`. Verify from the host:

```bash
PGPASSWORD='<password>' psql -h 127.0.0.1 -U prism -d prism -c '\conninfo'
```

### 3. lnd — make sure the cert covers `host.docker.internal`

Containers reach the host's lnd via `host.docker.internal`. The auto-
generated lnd `tls.cert` doesn't include that name in its SANs, which
makes the gRPC handshake fail. Two ways:

- **(a) regenerate cert with extra SAN**: stop lnd, delete tls.cert
  + tls.key, restart with `--tlsextradomain=host.docker.internal`.
  Channels and wallet are preserved.
- **(b) point prism at the host's loopback IP** that's already in
  the SAN: change `lndhost: "host.docker.internal:10009"` to e.g.
  `lndhost: "127.0.0.1:10009"` AND change `network_mode: host` on
  the prism services (loses container DNS, but works on Linux).

(a) is cleaner. The integration test script under
`/path/to/lnd/scripts/itest_sui_single_coin.sh` accepts `DOCKER=1`
to bake the SAN in automatically.

### 4. nginx — drop in the prism server block

```bash
# Copy the server block to nginx's conf.d (or sites-available).
sudo cp deploy/production/nginx-prism.conf /etc/nginx/conf.d/prism.conf

# Issue a TLS cert. Two options:
#
# Option A (easier, per-name): HTTP-01 via certbot. Each subdomain
# gets its own cert. Adding a new merchant means re-running certbot
# for that name.
sudo certbot --nginx -d prism.loka.cash

# Option B (one cert, manual): wildcard *.prism.loka.cash via DNS-01.
# Requires putting a TXT record in GoDaddy by hand every 90 days.
# certbot certonly --manual --preferred-challenges dns \
#                  -d '*.prism.loka.cash' -d prism.loka.cash

sudo nginx -t && sudo systemctl reload nginx
```

## Bring prism up

```bash
cd deploy/production

# 1. Edit prism.yaml.example → prism.yaml. Replace the placeholder
#    Postgres password and any other site-specific paths.
cp prism.yaml.example prism.yaml
$EDITOR prism.yaml

# 2. Build + start the 3 replicas (postgres + nginx are NOT in this
#    compose — they run on the host).
docker compose up -d --build

# 3. Confirm all 3 are healthy.
docker compose ps
docker compose logs prism-1 | grep -E 'Service config poller|Invoice reconciler|listening'
```

## Verify end-to-end

```bash
# Pull the admin macaroon from the shared volume (any replica works,
# they're all the same byte-for-byte).
docker run --rm -v production_prism-state:/state alpine \
    cat /state/admin.macaroon | xxd -ps -c 99999 > /tmp/prism-prod.mac

# Hit the LB (via host nginx).
curl -sf -H "Grpc-Metadata-Macaroon: $(cat /tmp/prism-prod.mac)" \
     https://prism.loka.cash/api/admin/info | jq

# Round-robin proof — fire 6 requests, watch nginx access log.
for i in $(seq 1 6); do
  curl -sf -o /dev/null -H "Grpc-Metadata-Macaroon: $(cat /tmp/prism-prod.mac)" \
       https://prism.loka.cash/api/admin/info
done
sudo tail -n 6 /var/log/nginx/access.log
# Should see request lines distributed across :8081, :8082, :8083.
```

## Operational notes

- **Backups**: `pg_dump prism > prism-$(date +%F).sql` is enough.
  The `agents_pay` database is backed up the same way; no special
  coordination needed since they're independent databases inside the
  same Postgres instance.
- **Postgres upgrade**: both services share the major version. If
  agents-pay needs to stay on PG15 while prism wants PG16, you can't
  share — fall back to running a second Postgres for prism (revive
  the bundled one in `ha-prototype/docker-compose.yml`).
- **Connection pool sizing**: each prism replica opens up to 25
  connections (`postgres.maxconnections`), so 3 replicas = ~75. Add
  agents-pay's pool. Postgres's default `max_connections` is 100;
  bump to 200 if you cut it close.
- **Adding a 4th replica**: copy the `prism-3` block in
  docker-compose.yml as `prism-4` with port `127.0.0.1:8084`, and
  add `server 127.0.0.1:8084` to the nginx upstream block.
