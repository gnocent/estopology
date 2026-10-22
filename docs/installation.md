*(Version française : [installation_fr.md](installation_fr.md))*

# Installing ES.Topo

This guide goes from a download to a service running over HTTPS with an administrator account. Follow it from top to bottom. The commands are for Linux; a section at the end covers Windows and macOS.

ES.Topo is a beta version (0.8.1-beta): install it on a test machine first.

Related guides: [declaring a cluster](clusters.md), [authentication](authentication.md), [high availability](high-availability.md), [operations](operations.md), [security](../SECURITY.md).

## What you need

- A Linux x86-64 machine that can reach your Elasticsearch clusters over HTTPS. Nothing else to install: the binary is static.
- Disk space for the history. Order of magnitude measured on simulated data: about 1 GB for 30 days of history of 373,000 shard copies. See [Operations](operations.md#sizing).
- For an installation exposed on the network: a TLS certificate and its key for the name of the machine.
- `root` rights to create the system user and the service.

## Choosing and verifying the binary

1. On the [releases page](../../../releases), download the binary for your platform and the `SHA256SUMS` file:

   | Platform | File | Use |
   |---|---|---|
   | Linux x86-64 | `estopo-linux-amd64` | production |
   | Windows x64 | `estopo-windows-amd64.exe` | trial, development |
   | macOS arm64 (Apple Silicon) | `estopo-macos-arm64` | trial, development |

   The simulator `essim` is provided under the same names (`essim-linux-amd64`, etc.). ES.Topo does not need it to work.
2. Verify the checksum, in the directory that holds both files:

   ```bash
   sha256sum -c SHA256SUMS --ignore-missing
   ```

   Expected result: `estopo-linux-amd64: OK`. If the command prints `FAILED`, the file is incomplete or altered: download it again and do not run it.
3. Make the binary executable and print its version:

   ```bash
   chmod +x estopo-linux-amd64
   ./estopo-linux-amd64 version
   ```

   Expected result: a line starting with `estopo 0.8.1-beta`.

The binaries are not signed: the checksum only proves that the file is the one that was published.

## First run by hand

Before installing the service, you can run the binary as it is:

```bash
./estopo-linux-amd64 serve
```

Without any setting, ES.Topo:

- listens on `127.0.0.1:8080`, over HTTP, reachable from this machine only;
- creates a `data` directory next to the binary;
- has no authentication: every visitor is an administrator;
- writes its logs to standard output. The first line looks like `level=INFO msg=starting version=0.8.1-beta listen=127.0.0.1:8080 data_dir=… tls=false secrets_dir=""`.

Open `http://127.0.0.1:8080` in a browser on the same machine. To stop, press Ctrl+C.

The binary accepts four commands:

| Command | Effect |
|---|---|
| `estopo serve` | starts the collection and the web interface |
| `estopo version` | prints the version |
| `estopo help` | prints the help |
| `estopo admin reset-password <user>` | gives a local account a new password (see [Authentication](authentication.md#resetting-a-password-from-the-command-line)) |

There are no command-line options: start-up settings go through environment variables, everything else is set in the interface.

## Environment variables

They are read at start-up. An invalid value prevents the start, with a message naming the variable.

| Variable | Default | Effect |
|---|---|---|
| `ESTOPO_LISTEN` | `127.0.0.1:8080` | Listen address and port. `0.0.0.0:8443` listens on every interface. |
| `ESTOPO_DATA_DIR` | `data` directory next to the binary | Data directory (history, configuration, accounts). Created if missing. |
| `ESTOPO_TLS_CERT`, `ESTOPO_TLS_KEY` | none | Certificate (full chain accepted) and private key, in PEM format. Both together enable HTTPS. |
| `ESTOPO_ADMIN_PASSWORD` | none | At the very first start only: creates the local account `admin` with this password (at least 12 characters) and enables local authentication. See [Securing the first start](#securing-the-first-start). |
| `ESTOPO_SECRETS_DIR` | none | When set, every file path typed in the administration (certificates, keys, password and API key files of the clusters, CA, LDAP files) must be inside this directory. The directory must exist. |
| `ESTOPO_ALLOW_INSECURE_AUTH` | `false` | `true` allows authentication, and the replication token, over plain HTTP on a non-local listener. For a test network only: passwords then travel in clear. |
| `ESTOPO_LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error`. |
| `ESTOPO_LOG_FORMAT` | `text` | `text` or `json`. |
| `ESTOPO_REPLICATION_TOKEN_FILE` | none | File of the shared high-availability token (at least 16 characters). See [High availability](high-availability.md). |
| `ESTOPO_STANDBY_OF` | none | URL of the primary: makes this instance a standby at its first start. |
| `ESTOPO_REPLICATION_CA` | system CAs | PEM file of the CA that signed the certificate of the other instance. |
| `ESTOPO_PPROF_LISTEN` | none | Local address (for example `127.0.0.1:6060`) of the Go profiler, for diagnostics. Not needed in normal use. |

## Installing as a systemd service

The repository provides two example files: [`deploy/estopo.service`](../deploy/estopo.service) and [`deploy/estopo.env.example`](../deploy/estopo.env.example). Download them next to the binary, then run the following steps as `root`.

1. Create the system user that will run the service:

   ```bash
   useradd --system --home-dir /var/lib/estopo --shell /sbin/nologin estopo
   ```

2. Install the binary under the name `estopo`:

   ```bash
   install -d -m 0755 /opt/estopo
   install -m 0755 estopo-linux-amd64 /opt/estopo/estopo
   ```

3. Create the data directory, which belongs to the service, and the configuration directories, which the service can read but not modify:

   ```bash
   install -d -o estopo -g estopo -m 0750 /var/lib/estopo
   install -d -o root -g estopo -m 0750 /etc/estopo /etc/estopo/secrets /etc/estopo/tls
   ```

   `/etc/estopo/secrets` will hold the secret files of the clusters (see [Declaring a cluster](clusters.md#the-secret-files)); `/etc/estopo/tls` will hold the certificate of the service.
4. Install the environment file and the unit:

   ```bash
   install -o root -g estopo -m 0640 estopo.env.example /etc/estopo/estopo.env
   install -m 0644 estopo.service /etc/systemd/system/estopo.service
   ```

5. Edit `/etc/estopo/estopo.env`. The example file listens on every interface over HTTPS (`ESTOPO_LISTEN=0.0.0.0:8443`): the certificate and the key must therefore be in place first (next section). For a first start without a certificate, replace the `ESTOPO_LISTEN` line with `ESTOPO_LISTEN=127.0.0.1:8080` and put a `#` in front of the two `ESTOPO_TLS_…` lines.
6. Start the service and check it:

   ```bash
   systemctl daemon-reload
   systemctl enable --now estopo
   systemctl status estopo
   journalctl -u estopo -n 20
   ```

   Expected result: the service is `active (running)` and the journal holds a `starting` line. If the service stops at once, the reason is in the journal: see [Troubleshooting](#troubleshooting).

The example unit restricts the service: it can only write to `/var/lib/estopo`, and it does not see the files under `/home` and `/root`. So put the certificates and the secrets under `/etc/estopo`, not in a home directory.

## Enabling HTTPS

HTTPS is required as soon as the service is reachable from another machine with authentication enabled.

1. Get from your certificate authority a certificate for the name of the machine (for example `estopo.example.org`) and its private key, in PEM format. The certificate file may hold the full chain: the server certificate first, then the intermediate certificates. The key must not be protected by a passphrase.
2. Copy them, readable by the `estopo` group only:

   ```bash
   install -o root -g estopo -m 0640 estopo-fullchain.pem /etc/estopo/tls/estopo-fullchain.pem
   install -o root -g estopo -m 0640 estopo.key /etc/estopo/tls/estopo.key
   ```

3. In `/etc/estopo/estopo.env`:

   ```sh
   ESTOPO_LISTEN=0.0.0.0:8443
   ESTOPO_TLS_CERT=/etc/estopo/tls/estopo-fullchain.pem
   ESTOPO_TLS_KEY=/etc/estopo/tls/estopo.key
   ```

4. Restart and check from another machine:

   ```bash
   systemctl restart estopo
   curl https://estopo.example.org:8443/healthz
   ```

   Expected result: `{"status":"ok","version":"0.8.1-beta","role":"primary","uptime":"…"}`. If `curl` rejects the certificate, add `--cacert` followed by the file of your CA. Also open the port in the firewall of the machine if needed.

Good to know:

- The certificate and the key are read at start-up. **After a renewal, restart the service** (`systemctl restart estopo`).
- ES.Topo listens on one port only. There is no redirection from HTTP to HTTPS.
- When ES.Topo serves HTTPS itself, it sends the `Strict-Transport-Security` header: the browser will then refuse HTTP for this name for one year.
- To put a reverse proxy in front of ES.Topo, see [Operations](operations.md#reverse-proxy).

## Securing the first start

Without authentication, every visitor is an administrator. Choose one of the two following paths **before** making the service reachable by others.

### Path A: `ESTOPO_ADMIN_PASSWORD` at the first start

This is the most direct path for a service installed over HTTPS from the outset.

1. Before the very first start (empty data directory), add to `/etc/estopo/estopo.env`, with HTTPS already configured:

   ```sh
   ESTOPO_ADMIN_PASSWORD=a-long-passphrase-of-your-own
   ```

   The password must have at least 12 characters. In a systemd environment file, prefer letters, digits and dashes: spaces, quotes, `#` and `$` have a special meaning there.
2. Start the service. The journal holds: `first start: account 'admin' created with ESTOPO_ADMIN_PASSWORD, local authentication enabled`.
3. Open the interface: it shows the "Sign in" screen. Sign in as user `admin` with this password.
4. Remove the `ESTOPO_ADMIN_PASSWORD` line from the environment file: it is no longer used. Then change the password in the interface (click on your name at the top right, "Change my password"), since the old one has been sitting in a file.

Rules applied by the code:

- The variable only acts when no authentication mode was ever saved **and** no account exists. Afterwards it is ignored.
- The start is refused if the password has fewer than 12 characters (even when the variable would be ignored).
- The start is refused if the transport is not safe: HTTPS is required, or a local listener (`127.0.0.1`), or `ESTOPO_ALLOW_INSECURE_AUTH=true`.
- It has no effect on an instance started with `ESTOPO_STANDBY_OF`: a standby gets its accounts from the primary.

### Path B: local listener, then creating an administrator in the interface

1. Start with `ESTOPO_LISTEN=127.0.0.1:8080` (the default value).
2. From your workstation, open an SSH tunnel to the machine, then open `http://127.0.0.1:8080` in your browser:

   ```bash
   ssh -L 8080:127.0.0.1:8080 you@estopo.example.org
   ```

3. Go to **Administration › Authentication**. Under "Users", type a "Name", choose the role "admin", type a password of at least 12 characters, then click "Save".
4. At the top of the same page, choose the "Mode" "Local accounts" and click "Save". The "Sign in" screen appears: sign in with the account you created.
5. Then configure HTTPS and the network listener (previous section), and restart.

If you switch to a network listener without HTTPS while authentication is enabled, the service refuses to start: this is intended.

The authentication modes, the accounts and the LDAP directory are described in [Authentication](authentication.md).

## What the first screen shows

- **Without authentication**: the "Clusters" page, with the banner "Authentication disabled: every visitor is an administrator." and the message "No cluster configured. Add one in the administration.".
- **With authentication**: the "Sign in" screen ("User" and "Password" fields, "Sign in" button), then the same "Clusters" page.

At the top right: the search, the theme, the language (button "Français"), the "Administration" link (for administrators), your name and "Sign out". Every screen has an "Understanding this screen" box.

Next step: [declare a first cluster](clusters.md).

## Windows and macOS

These binaries are meant for trying the product and for development. No service integration is provided for these systems.

- **The binaries are not signed.** Windows (SmartScreen) and macOS (Gatekeeper) may show a warning or block the first launch of a downloaded file. This behaviour has not been verified in detail for ES.Topo: follow the procedure of your system to allow a program that you have verified yourself.
- **Verifying the checksum**:
  - Windows (PowerShell): `Get-FileHash .\estopo-windows-amd64.exe -Algorithm SHA256`, then compare the value with the matching line of `SHA256SUMS`;
  - macOS: `shasum -a 256 estopo-macos-arm64`, then compare in the same way.
- **Running**:
  - Windows (PowerShell):

    ```powershell
    $env:ESTOPO_DATA_DIR = "C:\estopo\data"
    .\estopo-windows-amd64.exe serve
    ```

  - macOS:

    ```bash
    chmod +x estopo-macos-arm64
    ESTOPO_DATA_DIR="$HOME/estopo-data" ./estopo-macos-arm64 serve
    ```

Then open `http://127.0.0.1:8080`. Ctrl+C stops the program.

## Upgrading

1. Read the [changelog](../CHANGELOG.md) of the new version.
2. Back up the data directory, with the service stopped (see [Operations](operations.md#backup-and-restore)).
3. Replace the binary and restart:

   ```bash
   systemctl stop estopo
   install -m 0755 estopo-linux-amd64 /opt/estopo/estopo
   systemctl start estopo
   ```

Good to know:

- At start-up, ES.Topo updates the structure of its databases by itself when the new version requires it. This only goes forward: **going back to an older version is not supported**. To go back, restore the backup.
- With high availability, both instances must run the same version: see [High availability](high-availability.md#upgrading-both-instances).
- The upgrade from 0.8.1-beta to 0.8.1-beta has been exercised on a simulated data directory. Read the upgrade notes of the [changelog](../CHANGELOG.md#upgrading-from-080-beta) first (LDAP mode).

## Uninstalling

```bash
systemctl disable --now estopo
rm /etc/systemd/system/estopo.service
systemctl daemon-reload
rm -r /opt/estopo /etc/estopo
rm -r /var/lib/estopo        # deletes the whole history
userdel estopo
```

On the Elasticsearch side, delete the account, the API key or the role mapping created for ES.Topo.

## Troubleshooting

Start-up messages are in the journal (`journalctl -u estopo -n 50`) or on the terminal output. A configuration error is printed under `estopo: invalid configuration:`; the others as `msg=fatal err="…"`. In both cases the program stops with exit code 1.

| Message | Meaning and remedy |
|---|---|
| `ESTOPO_LISTEN: "…": …` | The address is not of the form `host:port`. Valid examples: `127.0.0.1:8080`, `0.0.0.0:8443`. |
| `ESTOPO_TLS_CERT and ESTOPO_TLS_KEY must be set together` | Only one of the two variables is set. |
| `ESTOPO_SECRETS_DIR: "…" is not a directory` | The directory does not exist. Create it. |
| `ESTOPO_ADMIN_PASSWORD must have at least 12 characters` | Make the password longer, or remove the variable if an account already exists. |
| `authentication mode "local" over plain HTTP on a non-loopback address is refused: …` (sometimes preceded by `ESTOPO_ADMIN_PASSWORD:`) | Authentication is enabled and the listener is neither local nor HTTPS. Configure `ESTOPO_TLS_CERT` and `ESTOPO_TLS_KEY`, or listen on `127.0.0.1`. |
| `replication over plain HTTP on a non-loopback address is refused: …` | Same rule for the replication token. |
| `listen tcp …: bind: address already in use` | The port is taken by another program. Change `ESTOPO_LISTEN`. |
| `listen tcp …: bind: permission denied` | A port below 1024 needs special rights. Use a port such as 8443. |
| `open /etc/estopo/tls/…: permission denied` or `no such file or directory`, after the `starting` line | The certificate or the key is missing, or not readable by the `estopo` user. Check the path, the owner and the permissions. |
| `tls: private key does not match public key` or `tls: failed to find any PEM data…` | The certificate and the key do not go together, or a file is not in PEM format. |
| `create data directory: …` or `unable to open database file` | The data directory is not writable by the user of the service. |
| `authentication settings: …` | The saved authentication setting cannot be read. ES.Topo refuses to start rather than behave as if there were no authentication. Restore a backup of the data directory. |

Other situations:

- **The page does not open from another machine.** The default listener is local. Check `ESTOPO_LISTEN` in the `starting` line of the journal, then the firewall.
- **The browser reports an untrusted certificate.** The certificate is not signed by an authority your workstation knows, or the file does not hold the intermediate certificates.
- **The interface shows up but every change fails with "cross-site request refused".** A reverse proxy alters the `Host` header: see [Operations](operations.md#reverse-proxy).
- **Administrator password lost.** See [Authentication](authentication.md#resetting-a-password-from-the-command-line).
