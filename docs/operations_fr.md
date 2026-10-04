*(English version: [operations.md](operations.md))*

# Exploiter ES.Topo

Ce guide s'adresse à la personne qui fait tourner le service : où sont les données, combien elles pèsent, comment les conserver et les sauvegarder, comment superviser l'instance, et comment se servir du simulateur. Il suppose l'installation décrite dans [Installation](installation_fr.md) : service `estopo`, données dans `/var/lib/estopo`, configuration dans `/etc/estopo`.

## Organisation du dossier de données

Tout ce qu'ES.Topo écrit se trouve dans le dossier de données (`ESTOPO_DATA_DIR`) :

```
<dossier de données>/
  meta.db                              configuration, comptes, sessions, acquittements, journal d'audit, catalogue
  clusters/<identifiant>/<AAAA>-W<ss>.db   l'historique : un fichier par cluster et par semaine ISO (UTC)
  spool/<identifiant>/…rec.zst         relevés bruts compressés, gardés 72 heures
  tmp/                                 copies en transit (haute disponibilité) ; vidé au démarrage
```

- Les fichiers `.db` sont des bases SQLite. Des fichiers `-wal` et `-shm` les accompagnent pendant qu'ils sont ouverts : ils en font partie.
- Le fichier de la **semaine en cours** est modifié à chaque relevé. Quand une nouvelle semaine commence, le fichier de la semaine précédente est **scellé** : il ne change plus. La page **Administration › Rétention et stockage** marque les fichiers scellés d'un cadenas.
- Le `spool` sert à rejouer, au redémarrage, un relevé reçu mais pas encore intégré, et à alimenter une instance standby.
- Ne sont **pas** dans ce dossier : le fichier d'environnement, le certificat du service et les fichiers de secrets (sous `/etc/estopo` dans l'installation décrite).

## Dimensionnement

Les seuls chiffres disponibles viennent de données simulées ; prenez-les comme des ordres de grandeur.

- **Disque** : environ 1 Go pour 30 jours d'historique de 373 000 copies de shards (trois clusters simulés). La taille dépend surtout du nombre de copies de shards et de l'agitation du cluster, pas du volume des données indexées.
- **Mémoire** : environ 1,4 Gio au maximum avec 15 clusters simulés en collecte, dont un de 245 000 copies de shards.
- La page **Administration › Rétention et stockage** donne, pour chaque cluster, la taille occupée, les « Octets/jour » et une « Projection 30 j » et « Projection 1 an » : c'est la mesure à suivre sur votre propre parc.
- Le nombre d'états reconstruits gardés en mémoire se règle dans **Administration › Système**, « États gardés en mémoire » : 64 par défaut, de 2 à 4096, environ 20 Mo par état d'un cluster de 250 000 shards. Le changement est pris en compte au redémarrage.

## Rétention et taille maximale

Dans **Administration › Rétention et stockage** :

| Champ | Défaut | Effet |
|---|---|---|
| « Rétention (jours) » | 30 | Durée de conservation, de 7 à 3650 jours. |
| « Taille maximale (Go, 0 = aucune) » | 0 | Plafond de taille du dossier de données entier. |

Fonctionnement :

- La rétention passe au démarrage, puis une fois par heure.
- Elle supprime des **semaines entières** : un fichier hebdomadaire n'est supprimé que lorsque toute sa semaine est sortie de la durée de rétention. L'historique consultable dépasse donc un peu la durée réglée.
- Quand la taille maximale est dépassée, les fichiers hebdomadaires les plus anciens, tous clusters confondus, sont supprimés jusqu'à repasser sous le plafond. La taille comptée est celle de tout le dossier (`meta.db`, `spool` et `tmp` compris), mais seuls les fichiers d'historique sont supprimés.
- Le fichier de la semaine en cours n'est jamais supprimé. Si le plafond reste dépassé malgré tout, une alerte s'affiche sur la même page.
- L'historique d'un cluster retiré de la configuration est conservé jusqu'à sa rétention.
- Le journal d'audit est gardé un an ; les relevés bruts du `spool`, 72 heures.

## Sauvegarde et restauration

**Quoi sauvegarder** : le dossier de données, et `/etc/estopo` (fichier d'environnement, certificat, secrets), qui n'en fait pas partie.

**Méthode sûre : service arrêté.**

```bash
systemctl stop estopo
tar -C /var/lib -czf /backup/estopo-data-$(date +%F).tar.gz estopo
systemctl start estopo
```

L'arrêt interrompt la collecte quelques instants ; au redémarrage, une sonde et une capture complète sont refaites.

**Sans arrêter le service**, seuls les fichiers scellés (semaines terminées) peuvent être copiés tels quels. `meta.db` et les fichiers de la semaine en cours sont en cours d'écriture : une simple copie peut être incohérente. ES.Topo ne fournit pas de commande de sauvegarde à chaud.

**Restaurer** :

```bash
systemctl stop estopo
rm -r /var/lib/estopo
tar -C /var/lib -xzf /backup/estopo-data-2026-10-01.tar.gz
systemctl start estopo
```

Vérifiez que les fichiers appartiennent toujours à l'utilisateur `estopo`. La période entre la sauvegarde et la restauration est absente de l'historique.

Une instance standby n'est pas une sauvegarde : elle reproduit aussi les suppressions. L'effet de la restauration d'un primaire sur sa standby n'a pas été vérifié ; après une restauration, cliquez sur « Reconstruire » sur la standby (voir [Haute disponibilité](high-availability_fr.md#reconstruire)).

## /healthz

`GET /healthz` répond toujours sans authentification, avec le code 200 dès que le service est en marche :

```json
{"status":"ok","version":"0.8.0-beta","role":"primary","uptime":"3h12m5s","clusters":4,"suspended":1,"stale":1}
```

| Champ | Signification |
|---|---|
| `status` | toujours `ok` quand le service répond |
| `version` | version du binaire |
| `role` | `primary`, `standby` ou `fenced` (ancien primaire en quarantaine) |
| `uptime` | durée depuis le démarrage |
| `clusters` | nombre de clusters en cours de collecte (absent s'il vaut 0, donc sur une standby) |
| `suspended` | clusters dont la collecte est suspendue après un changement d'identité (absent s'il vaut 0) |
| `stale` | clusters sans sonde depuis plus de 15 minutes (absent s'il vaut 0). Ce seuil est fixe : avec une période de sonde réglée au-delà de 15 minutes, le cluster est toujours compté ici. |

Le code HTTP ne reflète pas l'état de la collecte : pour alerter sur un cluster qui ne répond plus, lisez les champs ou utilisez `/metrics`.

## /metrics

`/metrics` expose des métriques au format texte de Prometheus. Il est **désactivé par défaut** (réponse 404).

Pour l'activer : **Administration › Système**, « Métriques Prometheus », cochez « Exposer /metrics ». L'effet est immédiat. Le réglage est répliqué sur une standby.

`/metrics` est servi **sans authentification**. Il ne contient aucune donnée de topologie (ni nom de nœud, ni nom d'index), mais il contient les identifiants des clusters configurés. Si cela vous gêne, filtrez l'accès à ce chemin dans votre pare-feu ou votre proxy inverse.

| Métrique | Étiquettes | Signification |
|---|---|---|
| `estopo_build_info` | `version`, `commit` | vaut 1 ; porte la version |
| `estopo_uptime_seconds` | | durée depuis le démarrage |
| `estopo_ha_role` | `role` | vaut 1 pour le rôle actuel (`primary`, `standby`, `fenced`) |
| `estopo_cluster_suspended` | `cluster` | 1 quand la collecte du cluster est suspendue (identité changée) |
| `estopo_probe_last_timestamp_seconds` | `cluster` | début de la dernière sonde légère |
| `estopo_probe_last_duration_seconds` | `cluster` | durée de la dernière sonde légère |
| `estopo_probe_last_ok` | `cluster` | 1 si la dernière sonde a réussi entièrement |
| `estopo_capture_last_timestamp_seconds` | `cluster` | début de la dernière capture complète |
| `estopo_capture_last_duration_seconds` | `cluster` | durée de la dernière capture complète |
| `estopo_capture_last_ok` | `cluster` | 1 si la dernière capture complète a réussi entièrement |
| `estopo_storage_bytes` | `cluster` | taille de l'historique du cluster |
| `estopo_replication_lag_seconds` | | standby : retard sur le primaire |
| `estopo_replication_last_sync_timestamp_seconds` | | standby : dernière synchronisation complète |
| `estopo_replication_blocked` | | standby : 1 quand elle refuse de répliquer (primaire remplacé ou vidé) |
| `estopo_ingest_records_total` | `cluster`, `kind` | relevés intégrés depuis le démarrage (`kind` : `light` ou `full`) |
| `estopo_ingest_errors_total` | `cluster`, `kind` | tentatives d'intégration en échec |
| `estopo_ingest_skipped_total` | `cluster`, `kind` | relevés abandonnés après trois échecs d'intégration : chacun est un trou dans l'historique |
| `estopo_ingest_seconds_total` | `cluster`, `kind` | temps passé à intégrer |
| `estopo_ingest_max_seconds` | `cluster`, `kind` | plus longue intégration d'un relevé |
| `estopo_ingest_queue` | | relevés en attente d'intégration |
| `estopo_go_heap_bytes` | | mémoire Go utilisée |
| `estopo_go_sys_bytes` | | mémoire obtenue du système par Go |
| `estopo_go_goroutines` | | nombre de goroutines |

Les métriques par cluster n'existent que sur l'instance qui collecte ; les métriques de réplication, que sur une standby.

Exemple de configuration de Prometheus :

```yaml
scrape_configs:
  - job_name: estopo
    scheme: https
    metrics_path: /metrics
    tls_config:
      ca_file: /etc/prometheus/ca.pem
    static_configs:
      - targets: ['estopo.example.org:8443']
```

Alertes suggérées :

| Condition | Signification |
|---|---|
| `estopo_probe_last_ok == 0` pendant plus de 15 minutes | un cluster ne répond plus correctement |
| `time() - estopo_probe_last_timestamp_seconds > 900` | la collecte d'un cluster est arrêtée |
| `estopo_cluster_suspended == 1` | un cluster a changé d'identité : décision à prendre (voir [Déclarer un cluster](clusters_fr.md#cluster-suspendu-après-un-changement-didentité)) |
| `increase(estopo_ingest_skipped_total[1h]) > 0` | des relevés ont été perdus |
| `estopo_replication_blocked == 1` | la standby a cessé de répliquer : voir [Haute disponibilité](high-availability_fr.md#réplication-bloquée) |
| `estopo_replication_lag_seconds > 900` | la standby prend du retard |

## Journaux

- ES.Topo écrit ses journaux sur la **sortie standard**, et nulle part ailleurs : pas de fichier, pas de rotation. Avec systemd, lisez-les avec `journalctl -u estopo`.
- `ESTOPO_LOG_FORMAT=json` produit une ligne JSON par message ; `ESTOPO_LOG_LEVEL=debug` détaille chaque relevé intégré.
- Il n'y a pas de journal des requêtes HTTP. Si vous en avez besoin, placez un proxy inverse devant ES.Topo.

Messages utiles :

| Message | Signification |
|---|---|
| `starting` | démarrage, avec la version, l'adresse d'écoute, le dossier de données |
| `apply configuration` (erreur) | la collecte d'un cluster n'a pas pu démarrer (fichier de secret illisible, URL refusée…) |
| `cluster identity changed, collection suspended` | un cluster a changé de `cluster_uuid` |
| `ingest` (erreur) | un relevé n'a pas pu être intégré ; il est réessayé |
| `retention` | des fichiers hebdomadaires ont été supprimés |
| `spool replayed` | au démarrage, des relevés en attente ont été intégrés |
| `LDAP authentication unavailable` | l'annuaire n'a pas pu être utilisé pour une connexion |
| `authentication unavailable` (erreur) | les réglages d'authentification ou les sessions n'ont pas pu être lus |
| `replication request with a wrong token` | une requête de réplication a présenté un mauvais jeton |
| `promoted to primary`, `demoted to standby`, `a more recent primary exists` | changements de rôle (haute disponibilité) |

## Ce qui demande un redémarrage

| Changement | Redémarrage |
|---|---|
| Une variable d'environnement | oui |
| Renouvellement du certificat ou de la clé HTTPS | oui |
| Contenu d'un fichier de secret d'un cluster (clé d'API, mot de passe, certificat, CA) | oui |
| Jeton ou CA de réplication | oui |
| « Captures complètes simultanées (tous clusters) » | oui |
| « États gardés en mémoire » | oui |
| Clusters, planification, hiérarchie, règles de familles, liste blanche | non, immédiat |
| Mode d'authentification, comptes, réglages LDAP, mot de passe du compte de service LDAP | non, immédiat |
| Rétention | non ; appliquée à la prochaine passe horaire |
| « Exposer /metrics » | non, immédiat |

L'arrêt se fait proprement sur `SIGTERM` (ce qu'envoie `systemctl stop`) ou Ctrl+C, avec dix secondes pour terminer les requêtes en cours.

## Proxy inverse

ES.Topo peut être placé derrière un proxy inverse (nginx, HAProxy, Apache…), à ces conditions :

- **Transmettre l'en-tête `Host` tel quel.** ES.Topo compare l'origine annoncée par le navigateur à cet en-tête et refuse les modifications si elles diffèrent (réponse 403, code `cross_site`).
- **Servir ES.Topo à la racine** du nom d'hôte : il n'y a pas de préfixe de chemin réglable.
- Laisser passer les réponses longues : un export ou une copie de partition peut durer plusieurs minutes.
- Ajouter vous-même l'en-tête `Strict-Transport-Security` si le proxy termine HTTPS : ES.Topo ne l'envoie que lorsqu'il sert HTTPS lui-même.

Exemple minimal pour nginx, à adapter, avec ES.Topo qui écoute sur `127.0.0.1:8080` sur la même machine :

```nginx
server {
    listen 443 ssl;
    server_name estopo.example.org;
    ssl_certificate     /etc/nginx/tls/estopo-fullchain.pem;
    ssl_certificate_key /etc/nginx/tls/estopo.key;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host $http_host;
        proxy_read_timeout 600s;
    }
}
```

Conséquences sur l'authentification (adresse du client vue par la limitation des tentatives, règle de transport, cookie) : voir [Authentification](authentication_fr.md#derrière-un-proxy-inverse). La réplication de la haute disponibilité se fait d'instance à instance : n'y intercalez pas un proxy qui modifierait les requêtes.

## Journal d'audit

**Administration › Système**, « Journal d'audit », liste les 200 dernières actions : date, « Auteur », « Action », « Cible », « Détails ». Les entrées sont gardées un an et répliquées sur une standby.

| Action | Ce qui s'est passé |
|---|---|
| `login`, `login.failure`, `login.ldap_unavailable` | connexion réussie, échouée (la cible est l'adresse du client), annuaire indisponible |
| `user.save`, `user.delete`, `user.password`, `user.reset_password`, `user.bootstrap` | gestion des comptes ; `reset_password` a pour auteur `cli`, `bootstrap` a pour auteur `system` |
| `auth.mode`, `auth.ldap_test` | changement de mode ou de réglages d'authentification ; test LDAP |
| `cluster.save`, `cluster.remove`, `cluster.purge`, `cluster.capture`, `cluster.resume`, `cluster.reattach` | configuration et actions sur un cluster |
| `family_rules.save`, `node.ack`, `node.ack.revoke` | règles de familles, acquittements |
| `retention.save`, `whitelist.save`, `tls.save`, `collector.save`, `cache.save`, `metrics.save` | réglages globaux |
| `ha.promote`, `ha.demote`, `ha.rebuild`, `ha.resume`, `ha.peer`, `ha.fenced` | haute disponibilité |

Sans authentification, l'auteur est toujours `anonymous`.

## Simulateur et données de démonstration

`essim` est fourni avec chaque version (`essim-linux-amd64`, `essim-windows-amd64.exe`, `essim-macos-arm64`). Il imite les API d'Elasticsearch qu'utilise ES.Topo, aux formats 7.17, 8.x et 9.x, pour essayer le produit, le démontrer ou le tester. Il n'a rien à faire sur une machine de production.

Quatre clusters types sont disponibles :

| Nom | Contenu |
|---|---|
| `small` | petit cluster, deux datacenters |
| `c` | 39 nœuds, environ 33 000 copies de shards |
| `b` | 91 nœuds, environ 96 000 copies de shards |
| `a` | 207 nœuds, environ 245 000 copies de shards (lent à simuler) |

### Historique fabriqué

`essim backfill` crée un dossier de données ES.Topo qui contient déjà plusieurs jours d'historique, terminés à l'instant présent, avec des incidents :

```bash
./essim-linux-amd64 backfill -data /tmp/estopo-demo
ESTOPO_DATA_DIR=/tmp/estopo-demo ./estopo-linux-amd64 serve
```

Par défaut : 14 jours, les clusters `b`, `c` et `small`, et le scénario `demo` (nœuds qui tombent et reviennent, machine perdue 3 heures, maintenance avec `allocation.enable=primaries`, mise à jour progressive de 8.19 à 9.5, nœuds reconstruits, setting d'index qui rend un index inallouable, panne d'API, suppression volontaire d'un index, datacenter perdu 50 minutes, et deux incidents encore en cours à la fin). Compter environ une demi-heure, surtout pour le cluster `b` ; les étapes s'affichent au fur et à mesure. Dans le dossier produit, les clusters sont désactivés (leurs simulateurs n'existent plus) : on explore l'historique.

| Option | Défaut | Effet |
|---|---|---|
| `-data` | obligatoire | dossier à créer |
| `-days` | 14 | nombre de jours d'historique |
| `-clusters` | `b,c,small` | clusters à simuler |
| `-versions` | `a=7.17.29,b=8.19.22,c=9.5.4,small=8.19.22` | version d'Elasticsearch par cluster |
| `-scenario` | `demo` | `demo`, un fichier JSON, ou `none` |
| `-end` | maintenant | fin de l'historique (date RFC 3339) |
| `-force` | non | remplace le dossier s'il existe déjà |

Le journal de la fabrication est écrit dans un fichier voisin du dossier, de même nom avec l'extension `.log`.

### Clusters simulés en direct

Lancez des simulateurs avec une horloge accélérée :

```bash
./essim-linux-amd64 -clusters b,c -insecure -speed 60
```

Chaque cluster écoute sur son propre port, à partir de 19200 : ici `b` sur `http://127.0.0.1:19200` et `c` sur `http://127.0.0.1:19201`. Déclarez-les dans **Administration › Clusters** avec ces URL, l'authentification « Aucune » et l'attribut machine `machine`.

| Option | Défaut | Effet |
|---|---|---|
| `-clusters` | `small` | clusters à simuler |
| `-version` | `8.19.22` | version d'Elasticsearch de tous les clusters |
| `-versions` | | version par cluster, par exemple `a=7.17.29,b=9.5.4` |
| `-listen` | `127.0.0.1` | adresse d'écoute |
| `-port` | 19200 | port du premier cluster ; les suivants se suivent |
| `-insecure` | non | HTTP sans authentification |
| `-pki` | `essim-pki` | dossier de la PKI de test, créée si elle manque |
| `-basic` | `estopo:estopo-test` | identifiants acceptés en authentification par mot de passe |
| `-apikey` | | clé d'API acceptée |
| `-start` | maintenant | date simulée de départ |
| `-speed` | 1 | secondes simulées par seconde réelle |
| `-scenario` | | incidents à jouer pendant le service : `demo` ou un fichier JSON dont les dates comptent depuis le début |

Sans `-insecure`, `essim` sert HTTPS avec une autorité de certification de test, et affiche les chemins de la CA, du certificat client et de sa clé à saisir dans le formulaire du cluster.

### Actions

`essim action` provoque un incident sur un cluster simulé en cours d'exécution :

```bash
./essim-linux-amd64 action -url http://127.0.0.1:19200 topology
./essim-linux-amd64 action -url http://127.0.0.1:19200 node-leave node=hot-dc1-m02-2 for=30m
./essim-linux-amd64 action -url http://127.0.0.1:19200 machine-leave machine=warm-dc2-m02
./essim-linux-amd64 action -url http://127.0.0.1:19201 fault path=/_cat/shards status=503 for=20m
./essim-linux-amd64 action -url http://127.0.0.1:19200 rolling-upgrade version=9.5.4 every=10m
```

`topology` liste les machines et les nœuds, pour choisir une cible. Une cible se désigne par la **fin de son nom**. Les durées sont en temps simulé : avec `-speed 60`, `for=30m` dure 30 secondes réelles. `for=…` annule l'action après ce délai.

| Action | Paramètres | Effet |
|---|---|---|
| `node-leave` | `node=` | le nœud s'arrête ; ses copies deviennent non assignées |
| `node-join` | `node=` | le nœud revient avec ses données |
| `node-replace` | `node=` | le nœud est reconstruit (nouvel identifiant, disque vide) |
| `machine-leave`, `machine-join` | `machine=` | tous les nœuds de la machine s'arrêtent, ou reviennent |
| `dc-leave`, `dc-join` | `dc=` | tous les nœuds du datacenter s'arrêtent, ou reviennent |
| `rolling-upgrade` | `version=`, `every=` (10m), `down=` (3m) | les nœuds redémarrent un à un dans la nouvelle version |
| `cluster-setting` | `key=`, `value=`, `transient=true` | change un cluster setting (valeur vide : le retire) |
| `index-setting` | `index=`, `key=`, `value=` | change un setting d'un index (ou du plus récent d'une famille) |
| `close-index`, `delete-index` | `index=` | ferme ou supprime l'index |
| `remote` | `remote=`, `connected=true` | déconnecte ou reconnecte un cluster distant (CCS) |
| `fault` | `path=`, `status=` | l'API répond ce code HTTP pour les chemins qui commencent par `path` |
| `clear-faults` | | retire toutes les pannes d'API |
| `uuid-change` | | le cluster change d'identité : ES.Topo suspend sa collecte |

Ces commandes passent par une interface de pilotage (`/_essim/…`) qui ne répond qu'aux appels venant de la machine elle-même. ES.Topo ne l'appelle jamais.

### Format d'un scénario

Un scénario est un fichier JSON : une liste d'étapes, chacune étant une action à une date.

```json
{
  "name": "mon-scenario",
  "steps": [
    {"at": "-2d09:15", "cluster": "b", "do": "dc-leave", "dc": "dc2", "for": "50m"},
    {"at": "-40m", "cluster": "*", "do": "node-leave", "node": "hot-dc1-m01-2"}
  ]
}
```

- `at` : la date de l'étape, comptée depuis le début de la simulation, ou à rebours depuis la fin quand elle est négative. Exemples : `40m`, `3h`, `6d`, `2d09:15` (2 jours, 9 heures et 15 minutes).
- `cluster` : `a`, `b`, `c`, `small`, ou `*` pour tous.
- `do` et les autres champs : l'action et ses paramètres, comme dans le tableau ci-dessus.

Les dates négatives n'ont de sens que pour `essim backfill`, qui connaît la fin de l'historique. En direct (`essim -scenario`), les dates doivent compter depuis le début.
