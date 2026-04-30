# Prism HA prototype (3 replicas + nginx + Postgres)

Local docker-compose stack that runs **3 prism replicas** behind an
**nginx load balancer**, sharing state via **Postgres** and a shared
basedir volume. Useful for:

- Verifying that prism is genuinely stateless above the DB layer.
- Stress-testing the round-robin path.
- Reproducing edge cases where two replicas race on the same DB row.
- Showing the multi-merchant routing layer still works under HA.

```
                                         host
                                       :10009 lnd (alice)
                                       :9998  demo backend
                                          ▲
                                          │ host.docker.internal
        ┌───────────┐                     │
client →│  nginx    │ → prism-1 ──┐       │
:8443   │  (TLS)    │ → prism-2 ──┼─→ shared lnd cert/macaroon (RO mounts)
        └───────────┘ → prism-3 ──┘
                          ▲
                          │ shared Postgres (replica state)
                          │ shared basedir vol (admin macaroon)
                          ▼
                       postgres :5432
```

## Shared state requirements (read this first)

A multi-replica prism deployment depends on three pieces of shared
state that are easy to get wrong if you only think about the database.
All three are wired correctly in this prototype — call out below for
when you adapt it to a real deployment.

### 1. Admin macaroon root key — must be on a shared volume

The admin macaroon's root key is stored on disk at
`<macpath>.rootkey`, **not** in the DB. Each replica that boots
without an existing rootkey file generates its own. Two replicas with
different rootkeys produce two different admin macaroons that won't
authenticate against each other — clients calling the LB get a 50%
"invalid macaroon" rate.

How to avoid: pin `admin.macaroonpath` to a path on a volume mounted
into every replica. The first to boot writes the rootkey and
admin.macaroon; the rest read them. In this prototype:

```yaml
# deploy/ha-prototype/prism.yaml
admin:
  macaroonpath: "/srv/prism/admin.macaroon"
# docker-compose.yml mounts the named volume `prism-state` at
# /srv/prism for every prism-N service.
```

Verify after boot: `md5sum` of `/srv/prism/admin.macaroon` and
`/srv/prism/admin.rootkey` is identical across replicas. If not,
you've fragmented the rootkey.

### 2. Service config — propagated via DB polling

Service create/update/delete via the admin API only writes to the DB
and updates the receiving replica's in-memory routing table. Other
replicas don't notice until they re-read the DB. Without periodic
refresh, the LB will route traffic to a replica that thinks the
service doesn't exist and respond 403.

How: each replica runs a background poller (`ServicePollInterval`,
default `30s`) that re-runs `mergeServicesFromDB` and reloads the
proxy when a stable hash of the service list changes. Set to `0` for
single-replica deployments to skip the polling tax.

A future Postgres-only optimisation could use LISTEN/NOTIFY for
sub-second propagation; polling is good enough at the prototype level
and works against any DB backend.

### 3. Invoice settlements — periodic reconciler as safety net

Every replica subscribes to the same lnd's invoice stream. On the
happy path each replica receives every settlement / cancellation
event; the `UpdateL402TransactionState` SQL is conditional on
`state='pending'`, so concurrent UPDATEs from N replicas converge on
the same final row.

The risk is a replica missing an event — transient lnd disconnect,
restart that lands after the event already fired, etc. To recover, a
periodic reconciler (`InvoiceReconcileInterval`, default `5m`)
re-runs the same sweep that runs at startup: page through every
invoice on lnd and re-fire terminal-state callbacks. Idempotency is
maintained by the same conditional SQL plus a per-hash dedupe map for
expirations.

Set `InvoiceReconcileInterval=0` to disable the periodic sweep (the
startup-time sweep still runs once on every boot).

## Prerequisites

On the host:
- alice's lnd (sui-lnd fork) listening on `127.0.0.1:10009`
- TLS cert at `/tmp/lnd-sui-test/alice/tls.cert`
- Macaroons at `/tmp/lnd-sui-test/alice/data/chain/sui/devnet/`
- Demo backend on `127.0.0.1:9998` (run `scripts/serve_demo_backend.sh`)
- Docker Desktop or compatible (uses `host.docker.internal` for host
  reachability — works out of the box on macOS / Win, Linux needs
  `host-gateway` which the compose file already declares).

## Bring it up

```bash
cd deploy/ha-prototype
./gen-tls.sh                 # one-off self-signed cert for nginx
docker compose up --build    # ~3-4 min on first build
```

Watch for:

- `postgres-1   | database system is ready to accept connections`
- `prism-1-1    | Starting the server, listening on 0.0.0.0:8080.`
- `prism-1-1    | Connected lnd reports chain="sui" network="devnet"`
- `prism-2-1` / `prism-3-1` come up after prism-1 is healthy
- `nginx-1     | Configuration complete; ready for start up`

## Verify the LB is round-robining

The admin macaroon is created by whichever replica wins the boot race
and stored in the shared `prism-state` volume.

```bash
# Pull the macaroon out of the volume (one-shot helper container).
docker run --rm -v ha-prototype_prism-state:/state alpine \
    cat /state/admin.macaroon | xxd -ps -c 99999 > /tmp/prism-ha.mac

# Hit the LB 6 times and watch nginx fan out.
for i in $(seq 1 6); do
  curl -sk -H "Grpc-Metadata-Macaroon: $(cat /tmp/prism-ha.mac)" \
       https://localhost:8443/api/admin/info > /dev/null
done

# Confirm round-robin in nginx access log.
docker compose logs nginx | grep upstream_addr
```

You should see `upstream_addr` cycle through `prism-1:8080`,
`prism-2:8080`, `prism-3:8080`.

## Verify shared state

```bash
# Create a service via the admin API on the LB. nginx will pick one
# replica (say prism-2). The state lands in Postgres.
curl -sk -X POST https://localhost:8443/api/admin/services \
    -H "Grpc-Metadata-Macaroon: $(cat /tmp/prism-ha.mac)" \
    -H "Content-Type: application/json" \
    -d '{"name":"ha-test","address":"127.0.0.1:9998","host_regexp":"^ha\\.local$","path_regexp":"^/probe$","price":1000000}'

# Read it back several times — every replica returns the same row.
for i in $(seq 1 5); do
  curl -sk -H "Grpc-Metadata-Macaroon: $(cat /tmp/prism-ha.mac)" \
       https://localhost:8443/api/admin/services | jq -r '.services[].name' \
    | tr '\n' ' '
  echo
done
```

## Kill a replica, traffic stays up

```bash
docker compose stop prism-2
# nginx upstream marks prism-2 as down on the next failed connect.
# Subsequent requests round-robin between prism-1 and prism-3.
for i in $(seq 1 6); do
  curl -sk -H "Grpc-Metadata-Macaroon: $(cat /tmp/prism-ha.mac)" \
       https://localhost:8443/api/admin/info > /dev/null
done
docker compose logs nginx | tail -10 | grep upstream_addr
```

`upstream_addr` should only show `prism-1:8080` and `prism-3:8080`
during the outage. Bring it back with `docker compose start prism-2`.

## Hit a paid endpoint through the LB

The same `manual_pay_l402.sh` script works against the LB — point it
at port 8443 and turn off macaroon discovery from the local basedir
(use the macaroon file we extracted above):

```bash
PRISM_HOST=localhost:8443 \
PRISM_BASEDIR=$(mktemp -d) \
SERVICE_HOST=service1.local \
PATH_SUFFIX=/probe \
ADMIN_MAC=/tmp/prism-ha.mac \
  ./scripts/manual_pay_l402.sh
```

(Some envs in that script assume PRISM_BASEDIR has admin.macaroon
inside; copy /tmp/prism-ha.mac into your tmp basedir as
`admin.macaroon` if it complains.)

## Tear down

```bash
docker compose down            # stop + remove containers
docker compose down -v         # also wipe Postgres + admin macaroon vols
```

## Caveats / TODO

- **autocert + multi-replica**: this prototype runs prism with
  `insecure: true` and lets nginx do TLS. Real prod with multiple
  replicas using prism's own autocert would race for Let's Encrypt
  challenges; you'd want a single ACME client (e.g. cert-manager) at
  the LB tier instead.
- **MPP session correctness under HA**: open/close events go through
  Postgres (`mpp_sessions` table) so any replica can serve any
  bearer call. Not yet stress-tested under concurrent
  open/bearer/close arrivals on different replicas.
- **Admin macaroon rotation**: regenerating means deleting
  `admin.macaroon` + `admin.rootkey` from the shared volume and
  restarting all replicas in lock-step (so the surviving in-process
  rootkey on a not-yet-restarted replica doesn't keep validating the
  old macaroon). There's no graceful multi-replica coordination yet.
- **Service config propagation latency**: changes via admin API land
  in the receiving replica's in-memory state immediately, but other
  replicas only pick them up at the next `ServicePollInterval` tick
  (default 30s). For deployments that need sub-second propagation,
  Postgres LISTEN/NOTIFY would be the next step.
