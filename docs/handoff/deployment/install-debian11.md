# Debian 11 Installation

This is the required compatibility target. Use a self-contained release binary.

## 1. DNS and TLS

Point a domain such as `drop.example.com` at the VPS.

Install Caddy or use an existing HTTPS reverse proxy. The application listens on loopback.

## 2. Create service identity and storage

```bash
sudo useradd \
  --system \
  --home /var/lib/sss \
  --shell /usr/sbin/nologin \
  sss

sudo install -d -o sss -g sss -m 0750 /var/lib/sss
sudo install -d -o sss -g sss -m 0750 /srv/sss
sudo install -d -o sss -g sss -m 0750 /srv/sss/staging
sudo install -d -o sss -g sss -m 0750 /srv/sss/live
sudo install -d -o sss -g sss -m 0750 /srv/sss/trash
sudo install -d -o root -g sss -m 0750 /etc/sss
```

## 3. Install binary

```bash
sudo install -o root -g root -m 0755 sss_linux_amd64 /usr/local/bin/sss
```

Verify:

```bash
sss version
```

## 4. Create password hash

```bash
read -rsp "SSS password: " SSS_PASS; echo
printf '%s' "$SSS_PASS" | sudo /usr/local/bin/sss hash-password --stdin
unset SSS_PASS
```

Place the result in `/etc/sss/config.toml`.

```bash
sudo install -o root -g sss -m 0640 \
  deployment/config.example.toml \
  /etc/sss/config.toml
sudoedit /etc/sss/config.toml
```

## 5. Install service

```bash
sudo install -o root -g root -m 0644 \
  deployment/sss.service \
  /etc/systemd/system/sss.service

sudo systemctl daemon-reload
sudo systemctl enable --now sss
sudo systemctl status sss
```

## 6. Configure Caddy

```caddyfile
drop.example.com {
    reverse_proxy 127.0.0.1:7070
}
```

Reload Caddy.

## 7. Validate

```bash
curl -fsS https://drop.example.com/healthz
curl -fsS -u sss https://drop.example.com/v1/info
```

Then run [`../evals/contract-smoke.sh`](../evals/contract-smoke.sh).

## 8. VPS-local agent access

Add the required local agent account to the `sss` group:

```bash
sudo usermod -aG sss <agent-user>
```

Start a new login session, then verify:

```bash
curl --unix-socket /run/sss/sssd.sock \
  http://localhost/healthz
```

Do not make `/srv/sss` generally writable to local agents.
