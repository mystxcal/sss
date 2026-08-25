# Installing SSS on Debian 11

The server is one static binary, one configuration file, one systemd unit, and
a reverse proxy for TLS. There is no database service, queue, or object store.

## 1. Create the service account and directories

```bash
sudo adduser --system --group --home /srv/sss --shell /usr/sbin/nologin sss
sudo mkdir -p /srv/sss /var/lib/sss /etc/sss
sudo chown sss:sss /srv/sss /var/lib/sss
sudo chmod 0750 /srv/sss /var/lib/sss
```

`/srv/sss` holds `staging`, `live`, and `trash`. **They must stay on one
filesystem**: publication and deletion are directory renames, and atomicity is
what guarantees that no code ever resolves to incomplete data.

## 2. Install the binary and aliases

```bash
sudo install -m 0755 sss-1.0.0-linux-amd64 /usr/local/bin/sss
sudo ln -sf /usr/local/bin/sss /usr/local/bin/sssend
sudo ln -sf /usr/local/bin/sss /usr/local/bin/ssrecv
sudo ln -sf /usr/local/bin/sss /usr/local/bin/sssd
sss version
```

The binary dispatches on the name it was invoked with, so the aliases need no
wrapper scripts.

## 3. Generate the base password hash

Pick one strong shared password for all trusted devices, then hash it. Only the
hash is stored on the server.

```bash
printf '%s' 'your-base-password' | sudo sss hash-password --password-stdin
```

## 4. Write the configuration

```bash
sudo cp packaging/config.example.toml /etc/sss/config.toml
sudo chown root:sss /etc/sss/config.toml
sudo chmod 0640 /etc/sss/config.toml
sudo editor /etc/sss/config.toml   # paste the hash into auth.password_hash
```

The configuration file is never world-readable: it holds the password hash.

## 5. Install the service

```bash
sudo cp packaging/systemd/sss.service /etc/systemd/system/sss.service
sudo systemctl daemon-reload
sudo systemctl enable --now sss
systemctl status sss
```

The unit creates `/run/sss` with mode 0750 owned by `sss:sss`. Any local agent
that should be able to use the zero-copy receive path must be in the `sss`
group:

```bash
sudo usermod -aG sss youragent
```

## 6. Terminate TLS with Caddy

```bash
sudo apt install -y caddy
sudo cp packaging/caddy/Caddyfile.example /etc/caddy/Caddyfile
sudo editor /etc/caddy/Caddyfile     # set your hostname
sudo systemctl reload caddy
```

The daemon binds `127.0.0.1:7070` only. Public traffic always arrives through
the proxy over HTTPS.

## 7. Verify

```bash
curl -fsS https://drop.example.com/healthz
curl -fsS -u sss https://drop.example.com/v1/info

export SSS_URL=https://drop.example.com
export SSS_PASSWORD='your-base-password'
scripts/contract-smoke.sh
```

On the VPS itself:

```bash
sudo -u sss curl -fsS --unix-socket /run/sss/sssd.sock http://localhost/local/status
```

## 8. Set up a client

Linux or macOS:

```bash
sss configure --url https://drop.example.com
export SSS_PASSWORD='your-base-password'
sss doctor
```

Windows (PowerShell):

```powershell
sss.exe configure --url https://drop.example.com
$env:SSS_PASSWORD = 'your-base-password'
sss.exe doctor
```

For unattended raw `curl`, use a protected netrc file instead of putting the
password on the command line:

```bash
printf 'machine drop.example.com login sss password your-base-password\n' > ~/.netrc
chmod 600 ~/.netrc
curl -fsS -n -F "file=@report.pdf" https://drop.example.com/s
```

## Rotating the base password

1. `sss hash-password` with the new password.
2. Replace `auth.password_hash` in `/etc/sss/config.toml`.
3. `sudo systemctl restart sss`.

Committed transfers are unaffected: codes remain valid, and the new password
authenticates immediately.
