# Ready-to-use Compose deployment

This directory is a complete Linux deployment template. It expects an existing
VSIX directory, a host TLS reverse proxy, Docker Engine with the Compose plugin,
and OpenSSL for generating credentials.

## Files

```text
deploy/
├── compose.yaml
├── .env                 # copy from .env.example; never commit
├── nginx/
│   └── private-marketplace-manager.conf.example
└── secrets/
    ├── api-token.txt    # generated; never commit
    └── admin-password.txt
```

## Set up

Copy this directory to an operator-owned location on the Linux server, then run:

```sh
cd /opt/private-marketplace-manager
cp .env.example .env
chmod 600 .env

install -d -m 0700 secrets
umask 077
openssl rand -hex 32 > secrets/api-token.txt
openssl rand -base64 24 > secrets/admin-password.txt

sudo install -d -o 10001 -g 10001 -m 0750 /var/lib/private-marketplace-manager
```

Edit `.env`. At minimum, replace `PUBLIC_URL`, `EXTENSIONS_HOST_DIR`, and
`STATE_HOST_DIR`. Set `MANAGER_UID` and `MANAGER_GID` to an account that can
write to both directories. The existing marketplace container can keep its
extension mount read-only.

Validate every resolved value before starting:

```sh
docker compose --env-file .env config
docker compose --env-file .env pull
docker compose --env-file .env up -d
docker compose --env-file .env ps
docker compose --env-file .env logs --tail 100 manager
curl --fail http://127.0.0.1:8080/health/ready
```

The sample publishes only to `127.0.0.1`. Configure the approved host reverse
proxy to terminate HTTPS and forward to `http://127.0.0.1:8080`, preserving the
original `Host` header. A complete Nginx server-block example is included in
`nginx/private-marketplace-manager.conf.example`; replace its hostname and
certificate paths. Set the proxy request-body limit to at least
`MAX_UPLOAD_BYTES` and its upload timeout to at least 30 minutes.

Read the full [deployment and recovery guide](../docs/deployment.md) before the
production pilot.
