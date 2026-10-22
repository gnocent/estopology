*(Version française : [authentication_fr.md](authentication_fr.md))*

# Authentication (advanced guide)

This guide describes who can sign in to ES.Topo and how: the three modes, the local accounts, the sessions, the LDAP directory and the reverse proxy case. To create the very first administrator, first see [Installation › Securing the first start](installation.md#securing-the-first-start).

Everything is set in **Administration › Authentication**, unless stated otherwise.

## The three modes

| Mode (label) | Who gets in | When to use it |
|---|---|---|
| "None (everybody is admin)" | Every visitor, without signing in, with administrator rights. A banner says so: "Authentication disabled: every visitor is an administrator." | Only on a local listener (`127.0.0.1`), for a trial. |
| "Local accounts" | The accounts created in ES.Topo. | Small team, or no directory. |
| "LDAP" | The people of the directory who belong to a configured group, **and** the local accounts. | Team managed in an OpenLDAP directory. |

The default mode is "None". To change it: choose the "Mode", then "Save". The change is immediate.

Switching to "Local accounts" or "LDAP" requires two things, checked when saving:

1. a safe transport (next section);
2. at least one **active local administrator account**. Otherwise the save is refused with the message `create a local admin account before enabling authentication (the way back in if the directory fails)`. So create the account first, then change the mode.

## The transport rule

A password must not travel in clear. The "Local accounts" and "LDAP" modes are only accepted in one of these cases:

- ES.Topo serves HTTPS (`ESTOPO_TLS_CERT` and `ESTOPO_TLS_KEY`);
- ES.Topo only listens locally (`ESTOPO_LISTEN=127.0.0.1:…` or `localhost:…`);
- `ESTOPO_ALLOW_INSECURE_AUTH=true` is set. A banner is then shown, including on the sign-in screen: "Warning: credentials travel in clear (HTTP without TLS, ESTOPO_ALLOW_INSECURE_AUTH).". For a test network only.

The rule is checked at two moments: when the mode changes (refused with the code `insecure_transport`), and when the service starts (the start is refused).

## Local accounts

### Roles

| Role | Can |
|---|---|
| "reader" | view every screen and export |
| "admin" | also: everything under **Administration** (clusters, retention, accounts, high availability…) |

### Creating, editing, disabling, deleting

Under "Users":

- **Create**: type a "Name", choose the role, type a password, then "Save". The name may contain no space, `/` or `\`.
- **Edit**: click ✎ in the line of the account; the form is filled in. Change the role, tick "Disabled" or type a new password ("Password (empty = unchanged)"), then "Save".
- **Delete**: "Delete" button of the line. You cannot delete your own account.

A new password, a role change or disabling the account **close the open sessions** of the account.

At sign-in, the name of a local account is compared ignoring case: `Alice` and `alice` name the same account.

The "Source" column shows `local` or `ldap`. `ldap` accounts are created automatically at the first sign-in of a person from the directory; their role is read again from the directory at each sign-in. You can disable an `ldap` account: it stays disabled, even if the directory accepts it.

### Password rule

One rule only: **at least 12 characters**. Passwords are stored as an argon2id hash, never in clear.

### Changing your own password

Click on your name, at the top right: the "Change my password" window asks for the "Current password" and the "New password (12 characters min.)". After "Save", the message "Password changed. Your other sessions were closed." is shown; the current session stays open. Only local accounts have a password in ES.Topo: the password of a directory account is changed in the directory.

### Resetting a password from the command line

When no administrator can sign in any more, give a local account a new password from the machine of the service, with the same system user and the same data directory as the service:

```bash
sudo -u estopo env ESTOPO_DATA_DIR=/var/lib/estopo /opt/estopo/estopo admin reset-password admin
```

Expected result: `New password for admin: …` (a random password, shown once). The command closes the sessions of the account and writes the action to the audit trail. It can be run while the service is running.

Good to know:

- the name must be written exactly as in the list of users;
- the command refuses a directory account (`account … is not a local account`);
- it does not re-enable a disabled account;
- on a standby instance, accounts are copied from the primary: do the reset on the primary.

### The last local administrator

As long as authentication is enabled, ES.Topo refuses to delete, disable or demote to reader the **last** active local administrator account (code `last_admin`). Without it, nobody could administer the instance any more, or get in if the directory fails.

## Sessions

- After sign-in, the browser receives a session cookie (`estopo_session`), not accessible to page scripts, limited to the same site, and marked `Secure` when the page is served over HTTPS. It disappears when the browser is closed.
- **Inactivity**: "Idle session expiry (hours)", 12 by default, from 1 to 720. The expiry is pushed back as the session is used, but only when less than half of the delay is left: in practice, an idle session expires between half the delay and the whole delay.
- **Maximum lifetime**: a session expires at the latest **7 days** after sign-in, even if it is used continuously. This duration cannot be set.
- "Sign out" closes the session.
- Changing the authentication mode closes every open session, except the one of the local administrator making the change (when the new mode is not "None").
- The chosen language and theme are saved with the account and follow it from one browser to another.

## Sign-in throttling

Failures are counted per 15-minute window:

- for each **user + client address** pair, and for each **client address**: from the fifth failure, you must wait before retrying. The wait is 2 seconds, then doubles at each new failure, up to 15 minutes;
- for each **user, whatever the address**: the same mechanism from the fiftieth failure, to slow down an attack spread over one account.

A successful sign-in resets the counters. During the wait, the screen shows "Too many attempts, retry in a few minutes.". An unreachable directory is not counted as a failure. The counters are in memory: a restart clears them. Checking the current password, during a password change, is subject to the same throttling.

## LDAP

Only **OpenLDAP** is tested (over LDAPS and StartTLS). The filters can be set, but operation with another directory, Active Directory included, has not been verified.

### Principle

At each sign-in of a person who has no local account:

1. ES.Topo connects to the directory, over TLS, and identifies itself with a **service account**;
2. it searches for the person with the user filter; it must find exactly one entry;
3. it checks the password by identifying itself as that person (*bind*);
4. it searches for the groups of the person and derives the role from them.

An empty password is refused before any call to the directory. Names are lower-cased.

### The fields

Choose the "LDAP" mode: the "LDAP directory" card appears.

| Field | Meaning | Default |
|---|---|---|
| "Server URLs (one per line, tried in order; ldaps:// or ldap:// with StartTLS)" | Addresses of the servers. Plain LDAP is refused. | required |
| "StartTLS (ldap:// URLs)" | Tick for `ldap://` URLs: the connection switches to TLS before any identification. | unticked |
| "CA file (PEM; empty = system CAs)" | CA that signed the certificate of the directory. A file, not a directory. | system CAs |
| "Expected TLS name (when it differs from the host)" | Name to verify in the certificate when it differs from the host name of the URL. | host name of the URL |
| "Service account DN" | Account with which ES.Topo searches for people and groups. A read right is enough. | required |
| "Service account password file" | Path of the file holding this password. It is read again at each connection: no restart after a rotation. | required |
| "User search base" | Branch in which to search for people (whole subtree). | required |
| "User filter ({user} = login name)" | Search filter; `{user}` is replaced with the typed name, protected against injection. | `(&(objectClass=inetOrgPerson)(uid={user}))` |
| "Account name attribute (read from the entry found)" | Attribute of the entry found that names the account in ES.Topo; it must have a single value. With a filter accepting the identifier or the address, `(\|(uid={user})(mail={user}))`, signing in with either opens the same account. For Active Directory: `sAMAccountName`. | `uid` |
| "Group search base" | Branch in which to search for groups. | required |
| "Group filter ({dn} = user DN, {user} = account name)" | Filter that finds the groups of a person. | `groupOfNames` groups (`member` attribute), `groupOfUniqueNames` (`uniqueMember`) and `posixGroup` (`memberUid`) |
| "Administrator groups (DN, one per line)" | Belonging to one of them gives the admin role. | at least one group, admin or reader |
| "Reader groups (DN, one per line)" | Belonging to one of them gives the reader role. | |

The directory timeout is 10 seconds; it cannot be set in the interface. If `ESTOPO_SECRETS_DIR` is set, the CA file and the password file must be inside this directory.

### Complete example with OpenLDAP

Example directory:

```
dc=example,dc=org
├── ou=services
│   └── cn=estopo                  service account
├── ou=people
│   ├── uid=alice                  inetOrgPerson
│   └── uid=bob                    inetOrgPerson
└── ou=groups
    ├── cn=estopo-admins           groupOfNames, member: uid=alice,ou=people,dc=example,dc=org
    └── cn=estopo-readers          groupOfNames, member: uid=bob,ou=people,dc=example,dc=org
```

1. On the ES.Topo machine, put the CA of the directory and the password of the service account in place:

   ```bash
   install -o root -g estopo -m 0640 ldap-ca.pem /etc/estopo/secrets/ldap-ca.pem
   install -o root -g estopo -m 0640 ldap-bind-password /etc/estopo/secrets/ldap-bind-password
   ```

2. Check that a local administrator account exists (section [Local accounts](#local-accounts)).
3. In **Administration › Authentication**, choose the "LDAP" mode and fill in:

   | Field | Value |
   |---|---|
   | Server URLs | `ldaps://ldap.example.org:636` |
   | CA file | `/etc/estopo/secrets/ldap-ca.pem` |
   | Service account DN | `cn=estopo,ou=services,dc=example,dc=org` |
   | Service account password file | `/etc/estopo/secrets/ldap-bind-password` |
   | User search base | `ou=people,dc=example,dc=org` |
   | User filter | *(empty: default filter)* |
   | Account name attribute | *(empty: `uid`)* |
   | Group search base | `ou=groups,dc=example,dc=org` |
   | Group filter | *(empty: default filter)* |
   | Administrator groups | `cn=estopo-admins,ou=groups,dc=example,dc=org` |
   | Reader groups | `cn=estopo-readers,ou=groups,dc=example,dc=org` |

4. Test before saving: type `alice` and her password in the two fields above the "Test" button, then click. Expected result: six ticked steps and "Authentication succeeded: role admin.".
5. Click "Save" (at the top of the page). Sign out and sign in as `alice`.

### The "Test" button

The test works on the values **as typed**, even if not saved, and saves nothing. Without a user, it only checks the connection and the service account ("Connection and service account OK (enter a user to test a full authentication)."). With a user and their password, it runs a full authentication and shows each step:

| Step | What is checked | If it fails |
|---|---|---|
| "TLS connection" | reaching a server and establishing TLS | address, port, firewall, CA, name in the certificate |
| "Service account bind" | DN and password of the service account | DN, content or permissions of the password file |
| "User search" | exactly one entry found | search base, filter; `no single matching entry` = no entry or several |
| "Password check" | bind with the typed password | password of the person |
| "Group search" | list of the groups found | group search base, filter |
| "Role" | role obtained | `member of no configured group` = the person is in no configured group |

The test is written to the audit trail. It is refused on a standby instance.

### From groups to roles

- Belonging to an administrator group gives the admin role; otherwise, belonging to a reader group gives the reader role; otherwise the sign-in is **refused**.
- DNs are compared the LDAP way: ignoring case and the spaces around commas.
- With the default filters, only **direct membership** counts: a group that is a member of another group is not followed.
- The role is read again at each sign-in. If it changed, the sessions opened with the previous role are closed.

### Keeping a local fallback account

In "LDAP" mode, local accounts keep working: when the typed name is that of a local account, the directory is not consulted. This is the way in when the directory is down or misconfigured. So keep at least one local administrator account, with a password stored in a safe place; ES.Topo prevents you from deleting the last one.

Consequence: a person of the directory whose name is also that of a local account cannot sign in with their directory password. Give local accounts names that do not exist in the directory (for example `fallback-admin`).

When the directory cannot be reached, the sign-in screen shows "Directory unavailable, retry later or use a local account.".

## Behind a reverse proxy

- **`Host` header**: the proxy must pass on the `Host` header received from the browser. ES.Topo refuses every change, and the sign-in itself, when the origin announced by the browser does not match this header (code `cross_site`, message `cross-site request refused: the origin is not this host`).
- **Client address**: ES.Topo does not read `X-Forwarded-For`. Behind a proxy, every connection comes from the address of the proxy: the per-address limit is therefore **shared by all users**, and a few failures by one delay the others.
- **Transport rule**: it applies to the listener of ES.Topo, not to that of the proxy. If the proxy is on the same machine, have ES.Topo listen on `127.0.0.1`. If it is on another machine, serve HTTPS between the proxy and ES.Topo as well.
- **`Secure` cookie**: when the proxy terminates HTTPS, the cookie is still marked `Secure`, based on the `https://` origin announced by the browser.
- ES.Topo must be served at the root of the site: there is no path prefix setting.

## Going back to no authentication

Choose the mode "None (everybody is admin)" and save. Every visitor becomes an administrator again; the accounts are kept and will be used again if you re-enable a mode. Only do this on a local listener.

## Troubleshooting

| Symptom | Cause and remedy |
|---|---|
| When saving the mode: `authentication mode "local" over plain HTTP on a non-loopback address is refused…` | See [The transport rule](#the-transport-rule). |
| When saving the mode: `create a local admin account before enabling authentication…` | First create a local account with the admin role. |
| `the last local admin account cannot be disabled or demoted` (or `deleted`) | Create another local administrator before touching this one. |
| `the password must have at least 12 characters` | Make the password longer. |
| "Invalid credentials." | Wrong name or password, disabled account, or person of the directory outside the configured groups. The detail is deliberately not given; use the "Test" button as an administrator. |
| "Too many attempts, retry in a few minutes." | See [Sign-in throttling](#sign-in-throttling). |
| "Directory unavailable…" | Directory unreachable, certificate refused or service account refused. The journal of the service holds `LDAP authentication unavailable` with the reason. Sign in with a local account. |
| `… not an existing file of the secrets directory (ESTOPO_SECRETS_DIR)` | The LDAP CA or password file is outside the secrets directory. |
| Every change fails with `cross-site request refused…` | Reverse proxy changing the `Host` header. |
| 503 answer `authentication is unavailable, retry later` | ES.Topo could not read its authentication settings or its sessions (database unavailable). It then refuses the request rather than let anyone in. Check the journal of the service. |
| No administrator can get in any more | [Reset a password from the command line](#resetting-a-password-from-the-command-line). |
| Change refused: `this instance is standby: changes are made on the primary` | Accounts and the mode are managed on the primary. |
