*(Version française : [SECURITY_fr.md](SECURITY_fr.md))*

# Security

This document describes, in plain words, what ES.Topo protects and how, what to set before exposing it, its known limits, and how to report a vulnerability. ES.Topo is a beta version (0.8.1-beta) and has not undergone any external security audit.

## Security model

### Towards Elasticsearch

- ES.Topo only **reads** the clusters. Its HTTP client refuses, at two levels, any request other than `GET` or `HEAD`.
- The account it uses only needs the privileges `monitor` (cluster) and `monitor` and `view_index_metadata` (indices): it can read neither the documents nor the mappings. See [Declaring a cluster](docs/clusters.md).
- The certificate of the cluster and its host name are always verified, with TLS 1.2 at least. There is no option to disable this verification.
- A password or an API key is never sent to an `http://` URL, except towards the local machine.
- Redirects are not followed, and the size of the responses is bounded.

### What the instance holds

The data directory holds the topology of your clusters over time: names and addresses of the nodes, names of the indices, settings, events. It also holds the configuration (URLs of the clusters, **paths** of the secret files), the local accounts (argon2id hashes of the passwords), the sessions (hashes of the tokens) and the audit trail. It holds no indexed documents, no clear-text passwords and no keys.

### Who can do what

| Who | What they can do |
|---|---|
| **A visitor who is not signed in**, authentication enabled | See the sign-in screen, call `/healthz` (version, role, number of clusters) and, if enabled, `/metrics`. Nothing else. |
| **A visitor**, authentication disabled (default mode) | **Everything**: they are an administrator. |
| **A reader** | View the history of every cluster and export it; change their own password. They do not see the administration (configuration, file paths, accounts, audit). |
| **An administrator** | Set everything: clusters, retention, accounts, authentication, high availability. They can make the server contact any URL, and make it read the files whose path they type (see [Known limits](#known-limits)). |
| **The holder of the replication token** | Read, through the replication interface, the whole configuration and the whole history, **including the password hashes** and the audit trail. They cannot change anything. |
| **A system account that can read the data directory** | Read everything the instance holds. |

### Default settings

- **Local listener.** Without any setting, ES.Topo only listens on `127.0.0.1:8080`. Exposing it requires setting `ESTOPO_LISTEN`.
- **Authentication over a safe transport only.** The "Local accounts" and "LDAP" modes are refused over plain HTTP on a non-local listener, at start-up as well as when the mode changes.
- **Refusal when in doubt.** If the authentication settings or the sessions cannot be read, requests are refused (503 answer) and the start fails: ES.Topo never behaves as if authentication were disabled.
- **Protection against requests from another site.** Every state-changing request must come from the same origin as the interface, carry a JSON body or a header specific to the interface, and, when authentication is enabled, an anti-CSRF token bound to the session. Otherwise it is refused (403 answer).
- **Sessions.** Cookie not accessible to scripts, limited to the same site, `Secure` over HTTPS; expiry after inactivity (12 hours by default) and maximum lifetime of 7 days; the other sessions are closed on a password change, a role change, or when the account is disabled, and every session on a change of the authentication mode; the standby closes its own the same way.
- **Passwords.** At least 12 characters, argon2id hash. Sign-in attempts are throttled per user and per address; a successful sign-in does not reset the count of the address.
- **Secrets as files.** Passwords, API keys, private keys and the replication token are never typed into the interface nor stored in the database: only their path is.
- **Browser headers.** Strict content security policy (no external resource, no embedding in another page), `X-Content-Type-Options: nosniff`, `Referrer-Policy: no-referrer`, and `Strict-Transport-Security` when ES.Topo serves HTTPS.
- **No outbound call** other than to your clusters, your directory and the other instance of the pair: no telemetry, no update check, no resource loaded from the Internet by the interface.
- **Audit trail** of sign-ins and administration actions, kept one year.
- **Exports**: in CSV files, cells that start like a formula are neutralised.

## Hardening checklist

Before opening the service to anyone but yourself:

1. **Enable authentication before exposing the service**: either with `ESTOPO_ADMIN_PASSWORD` at the first start, or by creating an administrator on a local listener. See [Installation](docs/installation.md#securing-the-first-start).
2. **Serve HTTPS** (`ESTOPO_TLS_CERT`, `ESTOPO_TLS_KEY`), directly or through a reverse proxy on the same machine.
3. **Set `ESTOPO_SECRETS_DIR`** to a directory that only holds the secrets of ES.Topo.
4. Run the service under a **dedicated system account**, with the provided systemd unit, which limits what the service can write and see.
5. Give the secret files and the environment file the mode `0640`, owner `root`, group of the service; the data directory, `0750`, owned by the service.
6. On the Elasticsearch side, use a **dedicated** account or API key, limited to the read-only role.
7. Do not use `ESTOPO_ALLOW_INSECURE_AUTH` outside a test network.
8. Remove `ESTOPO_ADMIN_PASSWORD` from the environment file after the first start, and change that password.
9. In LDAP mode, keep a local fallback account with a long password, stored in a safe place.
10. If you enable `/metrics`, filter access to this path if the ids of your clusters must not be public on your network.
11. With high availability, protect the token file like a private key, and enable authentication on both instances.
12. Use a firewall to limit the machines that can reach the port of the service.
13. Verify the SHA-256 checksum of the downloaded binaries, and keep ES.Topo up to date.

## Known limits

- **Without authentication, anything that reaches the service is an administrator.** On the default local listener, this includes every user and every program on the machine. It also includes a malicious web page opened in a browser on that machine, through the technique known as *DNS rebinding*: ES.Topo does not check the host name under which it is called. Enable authentication before exposing the service, and on any shared machine.
- **An administrator of the application can make the server read files of the machine.** The path fields (API key, password, certificate, CA) are read by the service; by combining them with a URL of their choice, an administrator can send their content to a server they control. Without `ESTOPO_SECRETS_DIR`, this holds for any file readable by the account of the service; with it, only for the files of that directory. The check takes place when saving and when testing: set the variable from the installation onwards.
- **An administrator can make the server send read requests to any address** it can reach (cluster, directory or watched instance URL).
- **`/healthz` is always public**; `/metrics`, when enabled, is too.
- **The replication token gives full read access**, password hashes included. If the primary has no authentication, neither has the standby, and any visitor can promote it.
- **Behind a reverse proxy**, ES.Topo does not read `X-Forwarded-For`: the per-address sign-in throttling is shared by all users, and the audit trail records the address of the proxy.
- **The binaries are not signed.** Only the published SHA-256 checksum lets you verify a download.
- **LDAP**: only OpenLDAP is tested.
- **The data directory is not encrypted.** Protect it with file system permissions and, if needed, with volume encryption.
- **No external security audit** has been carried out.

## Reporting a vulnerability

Do not open a public issue for a vulnerability.

Use the **private vulnerability reporting** of GitHub: in the repository, "Security" tab, then "Report a vulnerability". The report stays private between you and the maintainers until a security advisory is published.

Give the version (`estopo version`), the relevant configuration (authentication mode, HTTPS or not, reverse proxy, high availability), the steps to reproduce and the effect observed. Do not attach secrets or real data from your clusters.

Fixes are published in a new version, together with a security advisory of the repository. During the beta phase, only the latest published version receives fixes.
