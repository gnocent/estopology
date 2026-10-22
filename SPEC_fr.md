*(English version: [SPEC.md](SPEC.md))*

# ES.Topo — Spécification du projet

> Historisation légère de la topologie de clusters Elasticsearch (7.17 → 9.x) et exploration dans le temps : état d'un cluster à une date donnée, différences et chemin parcouru entre deux dates.

Statut : **v1.0 — conforme à l'implémentation** (2026-10-01). Cette version reporte dans la v0.1 de cadrage (2026-09-30) les précisions et les décisions prises pendant la réalisation. Les écarts par rapport à la v0.1 sont signalés par **(v1.0)**.

**v1.1** (2026-10-01) : refonte de l'interface après essai sur données simulées (aide dans les écrans, rendu, une page par élément de la topologie, vue logique enrichie, graphiques de famille). Ces changements sont signalés par **(v1.1)**.

**v1.2** (2026-10-04) : passe de sécurité et de robustesse avant la première version publique (0.8.0-beta), et mise en conformité de ce document avec le code : écoute locale par défaut, création du premier administrateur, dossier des secrets, sessions, requêtes venant d'un autre site, identifiant d'instance et blocage de la réplication, trois cibles de compilation. Ces changements sont signalés par **(v1.2)**.

**v1.3** (2026-10-22) : corrections de sécurité de la version 0.8.1-beta : identité LDAP lue dans l'entrée trouvée, sessions fermées au changement de mode et sur la standby, limitation des tentatives. Ces changements sont signalés par **(v1.3)**.

---

## 1. Problème et utilisateurs

- **Problème** : lors d'un incident de cluster (nœuds perdus, shards non assignés, rééquilibrage massif, upgrade qui dérape…), on manque d'une vue **historique** de la topologie : où étaient les shards, quels nœuds existaient, avec quels attributs, et quand cela a changé. Les outils de monitoring donnent des métriques, pas la topologie.
- **Utilisateurs** : les personnes qui exploitent des clusters Elasticsearch, pendant un incident et en post-mortem.
- **Dimensionnement visé** : une quinzaine de clusters, versions **7.17 à 9.x**, jusqu'à **~250 nœuds** et **~250 000 shards** par cluster, dizaines de milliers d'indices, plusieurs nœuds ES par machine physique, plusieurs datacenters, tiers hot/warm/cold, **CCS**. Les data streams, le tier frozen, les searchable snapshots et le CCR ne sont pas traités spécifiquement.
- **Fonctionnement sans accès réseau externe** : le déploiement se résume à copier un binaire. Tout (UI, polices, assets) est embarqué.

## 2. Périmètre

### 2.1 Dans le périmètre

- Collecte périodique et adaptative de la topologie de N clusters (liste et paramètres gérés dans l'admin).
- Stockage historique compact dans une base embarquée, avec rétention automatique (durée et taille max).
- UI web : état d'un cluster à une date T, différence et **chemin parcouru** entre deux dates, journal d'événements, pertes et disparitions, recherche, contraintes d'allocation, flux de déplacements.
- Administration intégrée (clusters, planification, rétention, stockage, état des collecteurs, utilisateurs, HA).
- Authentification : aucune / comptes locaux / LDAP (OpenLDAP en LDAPS), rôles lecteur et admin.
- Haute disponibilité simple : instance standby passive, promotion manuelle.
- Simulateur de clusters ES pour les tests, les mesures de performance et les données de démonstration (v1.2).

### 2.2 Hors périmètre

- Alerting / notifications (mails, webhooks…).
- Monitoring de performance (taux d'indexation/recherche, latences, GC…) : c'est le rôle de l'existant.
- Troubleshooting data : mappings, aliases, contenu des documents.
- Toute **écriture** sur les clusters ES : l'outil est strictement en lecture (§5.6).
- Gestion ILM, templates, snapshots/SLM.
- Bascule HA automatique.
- Packaging RPM (binaire nu + exemple d'unité systemd).
- Spécificités ECK/Kubernetes et Elastic Cloud.

## 3. Volumétrie et budget disque

Hypothèses de dimensionnement : ~1,1 M shards au total, ~300 k indices, ~2 000 nœuds, ~100 octets/ligne index compris, 2–5 % des shards en écriture active, partitions hebdomadaires.

| Poste | Par an (estimation) |
|---|---|
| Keyframes hebdomadaires (copie de l'état complet en début de partition) | ~6 Go |
| Intervalles de structure (créations, suppressions, relocations, changements d'état) | 4–7 Go |
| Tailles/docs des shards et indices (zone morte) | ~18 Go |
| Métriques nœuds toutes les 5 min (zone morte) + santé | 5–12 Go |
| Settings (dédupliqués), cluster settings, CCS, événements | < 2 Go |
| **Total** | **~35–45 Go/an — prévoir 100–200 Go pour 1 an, ~10 Go pour 30 jours** |

**(v1.0) Mesuré sur le jeu simulé** (§13, 30 jours backfillés, 373 000 copies de shards) : **1,015 Go** de partitions, soit environ **1×** l'estimation ramenée à ce jeu (0,98 à 1,25 Go). Une semaine pleine du cluster A (245 000 copies) pèse environ 150 Mo, soit environ 35 Go/an extrapolés aux hypothèses ci-dessus. Les keyframes coûtent plus que l'hypothèse de 100 octets par ligne (environ 250 octets par copie ouverte, index compris), mais les autres postes coûtent moins. Mesure faite sur données simulées uniquement. L'admin affiche, par cluster, les octets écrits par jour et une projection 30 j / 1 an (§9).

Remarque : un stockage naïf (photo complète chaque heure) ferait ~1 To/an. Le modèle par intervalles est retenu surtout pour la **performance** (diff, chemin, recherche restent des requêtes indexées sur peu de lignes), le gain disque étant secondaire.

## 4. Architecture et décisions

| Décision | Raison |
|---|---|
| Go, binaire unique statique `CGO_ENABLED=0`, cibles `linux/amd64`, `windows/amd64` et `darwin/arm64` | Aucune dépendance glibc → identique sur les distributions de type RHEL 8/9/10 ; déploiement = copie du binaire. |
| **SQLite pur Go** (`modernc.org/sqlite`), mode WAL | Pas de CGO. Volumétrie (dizaines de Go/an) sans besoin de moteur colonne. DuckDB et chDB écartés : CGO, taille, compatibilité glibc. |
| **Une base par cluster et par semaine ISO** (UTC) + une base `meta.db` | Écritures de 15 clusters sans contention ; rétention = suppression de fichiers, sans `VACUUM` ; requêtes bornées à un fichier. |
| **Modèle par intervalles de validité** + keyframe en début de partition | Seuls les changements sont écrits ; état à T = une requête ; diff/chemin = intervalles qui s'ouvrent/se ferment entre A et B. |
| Métriques (tailles, docs, CPU…) séparées de la structure, avec **zone morte** | Sinon chaque shard hot créerait un nouvel intervalle chaque heure. |
| Settings et blobs dédupliqués par hash (`blob` compressé zstd, `klauspost/compress`, pur Go) | Des milliers d'indices partagent les mêmes settings. |
| **Journal de captures** (« records » normalisés, compressés) comme unique entrée de l'ingestion | Ingestion déterministe : sert à la HA (log shipping), aux tests (rejeu) et au débogage. |
| Client HTTP brut (`net/http`), pas le client officiel | Seules des API `_cat`/`_cluster`/`_nodes` stables de 7.17 à 9.x sont utilisées ; le client typé varie selon les versions. |
| Décodage JSON **en flux** des grosses réponses, `Accept-Encoding: gzip` | `_cat/shards` sur 250 k shards = dizaines de Mo. |
| Front : **Svelte 5 + TypeScript + Vite**, D3 (modules ciblés) + **canvas** pour les grandes cardinalités ; build compilé **commité** dans le dépôt et embarqué via `go:embed` | Un `go build` seul suffit (Node requis uniquement pour modifier l'UI) ; rien à télécharger en prod air-gapped. |
| Export XLSX côté serveur (`excelize`, pur Go) | Pas de lib JS lourde, même code pour tous les exports. |
| LDAP via `go-ldap/ldap/v3` (pur Go) ; mots de passe locaux en argon2id (`x/crypto`) | Pur Go, standard. |
| Horloge injectable dans tout le code (collecte, ingestion, rétention) | Permet le backfill accéléré (30 jours simulés en minutes) pour tests et perfs. |

### 4.1 Arborescence du projet

```
cmd/estopo/         binaire principal (serve, version, admin reset-password)
cmd/essim/          simulateur de clusters ES (tests, perfs, démo)
internal/esclient/  client HTTP ES (TLS, mTLS, API key, basic, lecture seule)
internal/collector/ planification, sonde légère, capture complète, déclencheurs
internal/record/    format des records de capture (normalisés, zstd), spool
internal/store/     meta.db, partitions, ingestion par intervalles, rétention, réplication
internal/derive/    familles, hiérarchie, gravité, contraintes
internal/query/     état à T (cache), diff, chemin, pertes, journal, recherche
internal/api/       API REST JSON + exports (CSV/JSON/XLSX), /metrics
internal/auth/      local, LDAP, sessions, CSRF, limitation des tentatives
internal/ha/        réplication standby, promotion, quarantaine
internal/app/       assemblage (pipeline d'ingestion, boucles de rétention)
internal/sim/       moteur de simulation (utilisé par essim et les tests)
internal/i18n/      catalogues FR/EN côté serveur (messages d'événements, exports)
internal/…          (v1.2) autres paquets : backfill, bench, buildinfo, clock, config, logging, model, testpki
web/                sources Svelte ; web/dist/ compilé, commité, embarqué
deploy/             exemple d'unité systemd et de fichier d'environnement
testenv/            vrais ES (Docker), OpenLDAP, démarrage Rocky/Alma
docs/               (v1.2) guides (installation, clusters, authentification, haute disponibilité, exploitation), notes de version
scripts/            vérification, capture de réponses réelles, notices de licences
testdata/           réponses de vrais ES 7.17, 8.19 et 9.5
.github/workflows/  vérification à chaque envoi, publication des versions
```

## 5. Collecte

### 5.1 Sonde légère (défaut : toutes les 5 min, paramétrable)

| Appel | Usage |
|---|---|
| `GET /` | `cluster_uuid`, nom, version (détection d'un changement d'identité, §5.5) |
| `GET _cluster/health` | statut, nb nœuds/data, shards actifs/primaires/relocating/initializing/unassigned/delayed, pending tasks |
| `GET _nodes/stats/os,process,jvm,fs` (filter_path) | présence des nœuds (id, nom) + métriques : `process.cpu.percent` (CPU propre au nœud, pertinent avec les cpusets), `os.cpu` et load (globaux machine, affichés comme tels), `os.cgroup` si présent, heap %, uptime (→ heure de démarrage exacte), `fs.total`, `fs.io_stats` (débits IO par delta) |
| `GET _cat/allocation?format=json&bytes=b` | shards par nœud, `disk.indices`, `disk.used/avail/total/percent` |
| `GET _cluster/settings?flat_settings=true` | hash ; stocké intégralement s'il a changé |
| `GET _remote/info` | CCS : alias, mode, connecté, nb nœuds connectés |

### 5.2 Capture complète

| Appel | Usage |
|---|---|
| `GET _nodes` (filter_path) | nom, id, host, ip, transport/http addresses, rôles, attributs, version ES, `os.pretty_name`, noyau, arch, version JVM |
| `GET _cat/indices?format=json&bytes=b&expand_wildcards=all&h=uuid,index,status,health,pri,rep,docs.count,store.size,pri.store.size,creation.date` | indices |
| `GET _all/_settings/<liste blanche>?flat_settings=true&expand_wildcards=all` | settings d'index significatifs (§5.7) — **pas de mappings** |
| `GET _cat/shards?format=json&bytes=b&h=index,shard,prirep,state,docs,store,id,node,unassigned.reason,unassigned.at,unassigned.details` | shards (le champ `node` d'un shard `RELOCATING` contient la cible, parsée) |
| + les appels de la sonde légère | pour une photo cohérente ; **(v1.2)** les cluster settings sont alors lus avec `include_defaults=true` (watermarks, awareness) |

Chaque appel est chronométré ; durée et taille de réponse sont enregistrées et visibles dans l'admin (permet de mesurer le coût réel en prod, §18).

### 5.3 Déclenchement adaptatif

- **Capture complète déclenchée** quand la sonde légère voit un changement notable : statut de santé, ensemble des nœuds (apparition/disparition/nouvelle génération), compteurs relocating/initializing/unassigned au-delà d'un seuil (paramétrable), cluster settings modifiés.
- **Capture forcée** si aucune capture complète depuis `max_interval` (défaut 60 min), compté depuis le démarrage de la collecte tant qu'aucune n'a été tentée (des sondes qui échouent sans cesse n'en déclenchent jamais).
- La planification suit une horloge monotone : un recul de l'horloge système (NTP, VM restaurée) ne retarde pas les sondes ; les enregistrements gardent l'heure murale.
- **Délai minimum** entre deux captures complètes `min_interval` (défaut 10 min) : en pleine crise, l'outil ne doit pas charger un cluster qui souffre. Un déclenchement pendant ce délai est différé, pas perdu.
- Bouton « capturer maintenant » dans l'admin (respecte aussi le délai minimum, sauf forçage explicite admin).
- **Jitter** sur les planifications ; nombre de captures complètes simultanées limité (défaut 3, réglable dans l'admin, pris en compte au redémarrage) pour borner la mémoire et la charge.
- **(v1.0)** Le déclenchement compare la sonde à la **dernière capture complète réussie**. Un échec de capture complète compte pour `min_interval` (on ne martèle pas un cluster en souffrance) mais le déclenchement reste dû.

### 5.4 Identité des entités

- **Cluster** : `cluster_uuid` ; lié à l'entrée de configuration à la première capture.
- **Nœud** : identité = **nom**. Un changement d'`id` ES sous le même nom = **nouvelle génération** du même nœud (« vies » successives, affichées). Deux nœuds simultanés de même nom (anormal) : clé `(nom, id)`, signalé par l'événement `node_name_duplicate` (v1.0) au début de la situation.
- **Machine** : attribut de nœud configuré par cluster, sinon `host`, sinon IP.
- **Index** : **UUID** (un index supprimé puis recréé sous le même nom est une autre entité ; l'UI montre les deux vies). Correspondance nom → UUID à la capture via `_cat/indices`.
- **Copie de shard** : `(index, n° shard, p/r, nœud)`. Une copie qui change de nœud = fermeture + ouverture d'intervalle, appariées à l'analyse (relocation, promotion, perte). Copies non assignées : `(index, shard, p/r, NULL, rang)`.

### 5.5 Robustesse

- **Échec de capture** (timeout, 5xx, pas de master) : enregistré comme tel (`capture.status = failed`), événement `capture_failed`, **aucun intervalle n'est fermé** (inconnu ≠ inchangé). L'UI montre une bande « inconnu » sur la période.
- **Capture partielle** : si `_cat/shards` ou `_cat/indices` échoue, rien n'est fermé côté structure ; les métriques obtenues sont gardées.
- **Incohérence entre appels** (index créé entre `_cat/indices` et `_cat/shards`) : entité provisoire, résolue à la capture suivante.
- **Changement de `cluster_uuid`** derrière une même URL (VIP repointée, cluster reconstruit) : la collecte de ce cluster est **suspendue** (la sonde n'est pas enregistrée), alerte dans l'admin, action admin requise :
  - **accepter** : la configuration est liée à la nouvelle identité à la capture suivante ;
  - **rattacher comme nouveau cluster** : une nouvelle configuration reprend les mêmes paramètres de connexion, et l'ancienne est désactivée en gardant son historique.
- **(v1.0)** Un échec de `_all/_settings` laisse la capture `ok` avec des settings « inconnus » (les précédents sont gardés). Un échec de `_nodes`, `_cat/indices` ou `_cat/shards` la rend `partial` (rien de structurel n'est fermé). **(v1.2)** Une réponse de `_nodes` ou de `_nodes/stats` qui signale des nœuds en échec, ou qui ne contient aucun nœud, est traitée comme un échec de l'appel : aucun nœud n'est déclaré parti (un échec de `_nodes` rend la capture `partial`). Un échec d'un appel annexe (`_nodes/stats`, `_cat/allocation`, `_cluster/settings`, `_remote/info` hors erreur 400) n'écarte que sa section, inconnue et non vide (aucun nœud déclaré parti, aucun remote fermé, les cluster settings précédents gardés) : une sonde est alors `partial`, une capture complète reste `ok` et garde sa structure, l'erreur étant notée.
- Indices système et cachés inclus ; filtre UI « masquer les indices système » (actif par défaut, porté par l'URL, valable pour toutes les vues).
- Horodatage : horloge du collecteur en UTC (ms) ; les dates fournies par ES (`creation_date`, `unassigned.at`, démarrage de nœud) sont stockées à part comme **dates exactes**.

### 5.6 Connexion, sécurité et privilèges

- TLS 1.2+ ; authentification : **certificat client (défaut)**, API key, basic. Secrets et clés **sur disque**, référencés par chemin ; passphrase optionnelle via fichier. **(v1.2)** Une authentification par API key ou basic sur une URL `http://` est refusée, sauf vers loopback. Les réponses sont bornées (2 Gio décodés). Si `ESTOPO_SECRETS_DIR` est défini, tous les chemins de fichiers saisis dans l'admin doivent se trouver dans ce dossier (§12.1).
- CA : une **CA par défaut** (fichier PEM ou dossier dont tous les `*.pem`/`*.crt`/`*.cer` sont chargés, racine + intermédiaires de l'entreprise ; réglée dans l'onglet Clusters de l'admin et vérifiée à l'enregistrement), surchargeable par cluster ; option « ajouter le magasin système ». Sans aucune CA configurée : magasin système.
- Clés privées PKCS#8 chiffrées (PBES2, défaut d'OpenSSL) acceptées, passphrase lue dans un fichier.
- Plusieurs URLs par cluster possibles (repli), généralement une seule VIP.
- Le client n'autorise **que `GET` et `HEAD`** (garde-fou codé et testé).
- Privilèges : cluster `monitor` ; indices `monitor` + `view_index_metadata` sur `*` (avec `allow_restricted_indices`). **(v1.0) Confirmés** sur 7.17.29, 8.19.22 et 9.5.4 avec les trois authentifications. Le test de connexion de l'admin vérifie les privilèges (`_security/user/_has_privileges`).
- `_remote/info` répond 400 si le nœud interrogé n'a pas le rôle `remote_cluster_client` : traité comme « CCS indisponible », pas comme un échec de capture.

### 5.7 Liste blanche des settings d'index (défaut, modifiable dans l'admin)

**(v1.2)** `index.number_of_shards`, `index.number_of_replicas`, `index.auto_expand_replicas`, `index.routing.allocation.*` (couvre `include`/`exclude`/`require`, `total_shards_per_node`, `enable` et `_tier_preference`), `index.routing.rebalance.enable`, `index.blocks.*`, `index.unassigned.node_left.delayed_timeout`, `index.priority`, `index.refresh_interval`, `index.translog.durability`, `index.lifecycle.name`, `index.version.created`, `index.creation_date`, `index.uuid`, `index.hidden`.

Cluster settings : **tous** (persistent + transient), historisés avec diff.

## 6. Modèle de données

### 6.1 Fichiers

```
<data_dir>/
  meta.db                                        configuration, utilisateurs, acquittements, catalogue des partitions
  clusters/<cluster_id>/<AAAA>-W<ss>.db             une partition par cluster et semaine ISO (UTC)
  spool/<cluster_id>/<ms>-<seq>-<kind>.rec.zst      records de capture (72 h)
  tmp/                                           instantanés de partitions en transit (HA)
```

### 6.2 `meta.db`

`cluster` (config, état actif/suspendu, uuid lié, planification, hiérarchie, attribut machine, référence de nœuds), `setting` (réglages globaux : rétention, liste blanche, authentification et LDAP, CA par défaut, parallélisme, taille du cache, `/metrics`), `family_rule` (regex par cluster), `user`, `session`, `node_ack` (acquittements, §7.2), `partition` (catalogue : cluster, semaine, taille, scellée), `ha_state`, `audit` (actions admin), `alert`.

**(v1.0)** `session`, `partition`, `alert` et `ha_state` décrivent l'instance elle-même : ils ne sont pas répliqués vers la standby (§11). L'audit est conservé **1 an** ; les sessions expirées sont purgées toutes les heures.

### 6.3 Partition (cluster × semaine) — tables principales

Temps en entier (ms UTC). `valid_to IS NULL` = intervalle ouvert.

- `capture(id, ts, kind[light|full|keyframe], trigger, status[ok|partial|failed], duration_ms, api_stats json, error)`
- `health(ts, status, nodes, data_nodes, active_primary, active, relocating, initializing, unassigned, delayed_unassigned, pending_tasks)` — à chaque sonde
- `node(id, name, es_id, generation, first_seen)` — dimension
- `node_presence(node_id, valid_from, valid_to)` — résolution 5 min (sonde légère)
- `node_info(node_id, valid_from, valid_to, roles_mask, tiers, ip, transport_port, http_addr, host, machine, es_version, os_name, os_kernel, jvm_version, attrs_blob)` — résolution capture complète
- `node_metric(node_id, ts, proc_cpu, os_cpu, load1, cgroup_cpu, heap_pct, disk_total, disk_used, disk_avail, disk_indices, shards, io_r_ops, io_w_ops, io_r_bytes, io_w_bytes)` — `WITHOUT ROWID`, zone morte
- `idx(id, uuid, name, family, creation_date, version_created)` — dimension
- `index_iv(index_id, valid_from, valid_to, status, pri, rep, settings_blob)`
- `index_metric(index_id, ts, docs, store_bytes, pri_store_bytes)` — zone morte
- `shard_iv(id, index_id, shard, prirep, node_id, slot, state, reloc_target_node_id, unassigned_reason, unassigned_at, unassigned_details_blob, valid_from, valid_to)`
- `shard_metric(index_id, shard, prirep, node_id, ts, docs, store_bytes)` — zone morte (défaut : ±5 % ou ±1 Go)
- `cluster_settings_iv(valid_from, valid_to, blob)`
- `remote_iv(alias, valid_from, valid_to, mode, connected, nodes_connected, blob)`
- `event(id, ts_lo, ts_hi, exact_ts, type, severity, subject_kind, subject_ref, group_ref, payload json)`
- `blob(hash, data)` — contenu normalisé compressé

Index (au minimum) : `valid_from` et `valid_to` sur chaque table d'intervalles ; index partiel `WHERE valid_to IS NULL` ; `shard_iv(index_id, shard)` ; `shard_iv(node_id, valid_from)` ; `event(ts_hi)`, `event(type, ts_hi)`, `event(subject_kind, subject_ref)`. Écritures par transactions groupées (une par capture).

### 6.4 Sémantique temporelle

- **État à T** = état observé par la **dernière capture complète réussie ≤ T** (reproductible). Requête : `valid_from ≤ Tc AND (valid_to IS NULL OR valid_to > Tc)`. Dans une partition, un intervalle ouvert signifie « encore ouvert à la fin de la période couverte ».
- **Fenêtre d'incertitude** d'un changement = `]dernière capture où l'ancien état a été vu, première capture où le nouveau est vu]` (`ts_lo`, `ts_hi`). L'UI affiche « entre 14:05 et 14:10 », ou une date **exacte** (badge) quand ES la fournit : création d'index (`creation_date`), désassignation (`unassigned.at`), arrivée d'un nœud (horodatage − uptime).
- Présence des nœuds et santé : résolution de la sonde (5 min). Placement des shards : résolution des captures complètes (≤ 60 min, ≤ 10 min en cas d'agitation).
- **Keyframe** : au **premier record** d'une nouvelle semaine (sonde légère comprise, v1.0), tous les intervalles ouverts sont recopiés dans la nouvelle partition **avec leur date de début d'origine**, ainsi que la **dernière valeur de chaque métrique** (sans quoi, avec la zone morte, le début de semaine n'aurait pas de métriques). Une partition est lisible seule.
- **(v1.0)** Les settings d'index statiques (uuid, `creation_date`, `version.created`, hidden) sont portés par la dimension `idx`, pas par le blob de settings. Le premier import d'un cluster ne produit pas d'événements de création. Les copies non assignées sont distinguées par un rang (`slot`) parmi celles du même `(index, shard, p/r)`, trié par date de désassignation.
- Diff/chemin **à cheval sur plusieurs partitions** : reconstruction par clés naturelles (nom de nœud, UUID d'index), le journal d'événements étant continu d'une partition à l'autre.

### 6.5 Événements dérivés (à l'ingestion)

`health_changed`, `node_left`, `node_joined`, `node_new_generation`, `node_name_duplicate` (v1.0), `node_upgraded` (version ES/OS/JVM), `node_attrs_changed`, `machine_lost` (tous les nœuds d'une machine partis dans la même fenêtre), `index_created`, `index_deleted`, `index_closed`, `index_opened`, `index_settings_changed`, `cluster_settings_changed`, `shard_relocated`, `shard_promoted`, `shard_lost`, `shard_unassigned`, `shard_assigned`, `remote_disconnected`, `remote_connected`, `capture_failed`, `cluster_uuid_changed`.

### 6.6 Rétention et taille maximale

- Paramètres admin : durée (défaut 30 jours, jusqu'à ≥ 1 an) et taille max du dossier de données (défaut : aucune).
- Granularité : la **semaine** (une partition est supprimée quand toute sa période est hors rétention).
- Taille max dépassée : suppression des partitions les plus anciennes, tous clusters confondus, jusqu'à repasser sous le seuil (la dernière partition de chaque cluster n'est jamais supprimée : jusqu'au premier record d'une semaine, celle de la semaine précédente porte encore l'état que la keyframe reprend) ; alerte et bandeau dans l'admin. **(v1.0)** La taille comptée est celle de tout le dossier de données, `meta.db` et spool compris ; seules les partitions sont supprimées.
- Cluster retiré de la configuration : historique conservé jusqu'à expiration. **(v1.2)** Le bouton « purger » (admin) est dans le formulaire du cluster, qui n'est plus accessible une fois le cluster retiré : purger avant de retirer. Un cluster encore collecté recommence son historique par une capture complète (déclencheur `first`, sous réserve de `min_interval`).

### 6.7 Records de capture et déterminisme

Chaque sonde et capture produit un **record** normalisé (JSON trié, zstd) écrit atomiquement dans le spool, puis ingéré. L'ingestion ne dépend que des records et de l'état précédent. Conséquences : la standby rejoue les mêmes records (§11) ; les tests d'ingestion sont des rejeux ; au démarrage, les records spoolés mais pas encore ingérés sont rejoués. Un outil de rejeu manuel (correction d'un bug d'ingestion récent) est **reporté** (v1.0).

Les records d'un cluster sont ingérés dans l'ordre des dates : un record plus ancien que le dernier ingéré est refusé. Si l'horloge de l'instance a sauté en avant puis est revenue (NTP, VM restaurée), les records datés pendant le saut mettent l'historique en avance sur l'horloge, et les suivants sont refusés jusqu'à ce que l'horloge le rattrape : un trou dans l'historique. Il n'est pas corrigé, mais signalé quand le dernier record ingéré a plus d'une heure d'avance sur l'horloge : dans l'erreur de refus, dans la métrique `estopo_history_ahead_seconds` et par un bandeau de la page Collecteurs (§9).

## 7. Calculs à l'affichage (rien de stocké)

### 7.1 Gravité (ordre décroissant, rouge → vert)

1. Primaires non assignés (cluster rouge)
2. Nœuds manquants par rapport à la référence (§7.2)
3. Réplicas non assignés
4. Nœuds au-dessus du watermark `high`
5. Contraintes d'allocation infaisables (§7.5)
6. Shards en initialisation / relocation

« Inconnu » (capture en échec) : gris. Coloration par défaut de la topologie : **shards non assignés**, puis gravité ; le sujet le plus préoccupant est affiché en rouge en tête.

### 7.2 Référence du nombre de nœuds et acquittement

- Référence = ensemble des nœuds vus sur les **7 derniers jours glissants**.
- Un nœud supprimé durablement se « résorbe » seul au bout de 7 jours ; pour l'effacer tout de suite : **acquitter** (nœud retiré à partir de la date X, commentaire, auteur). Réservé au rôle admin, tracé dans l'audit, annulable.

### 7.3 Familles d'indices

Détection automatique des suffixes de date et de rollover (`yyyy.MM.dd`, `yyyy.MM`, `yyyy-MM-dd`, `yyyyMMdd`, `-000001`, …) → nom de famille. Regex par cluster pour surcharger, avec aperçu du regroupement dans l'admin.

**(v1.1)** Précisions :
- La famille est déduite **du seul nom de l'index** (ni alias, ni data stream, ni ILM). Les suffixes sont retirés en boucle (`.ds-logs-app-2026.09.28-000001` → `.ds-logs-app`) ; un nom sans suffixe reconnu est sa propre famille.
- Un suffixe de quatre chiffres n'est pris pour une année qu'entre **1990 et 2099** : `metrics-8080` reste sa propre famille.
- La famille est recalculée à la lecture avec les règles en vigueur : changer une règle regroupe aussi l'historique.
- **Historique d'une famille** : `GET /clusters/{id}/families/{famille}/history` échantillonne, entre deux dates, la somme des documents, de la taille sur disque et le nombre d'indices (dernier relevé connu de chaque index ; un index supprimé cesse de compter).

### 7.4 Hiérarchie de navigation

Niveaux configurables par cluster, parmi : attribut de nœud, tier, machine, nœud. **(v1.2)** Dans l'admin, la hiérarchie se saisit comme une liste de mots séparés par des virgules : `tier`, `machine` et `node` sont les niveaux intégrés, tout autre mot est un nom d'attribut de nœud (exemple : `dc, tier, machine, node`) ; dans l'API, chaque niveau est un objet (`{"kind":"attr","attr":"dc"}`, `{"kind":"tier"}`…). Défaut : attributs de `cluster.routing.allocation.awareness.attributes` dans l'ordre, puis tier, machine, nœud.

### 7.5 Contraintes d'allocation (par index, à la date T)

Pour chaque index : copies requises par shard (`1 + replicas`) comparées aux nœuds éligibles (tier préféré disponible, `include`/`exclude`/`require` d'index et de cluster, nœuds sous watermark `high`), à `total_shards_per_node`, à l'awareness (forcée ou non) et à `same_shard.host` (copies d'un même shard sur une même machine). Comme dans Elasticsearch : un `require` à plusieurs valeurs exige qu'elles correspondent toutes (une seule suffit pour `_ip`, `_host_ip`, `_publish_ip`), et un nœud sans l'attribut ne satisfait aucun `require`, même `*` ; le watermark `high` est ignoré quand `cluster.routing.allocation.disk.threshold_enabled` vaut `false` ; à partir de la 8.5, un watermark en pourcentage est plafonné par `cluster.routing.allocation.disk.watermark.high.max_headroom` (150 Go quand le watermark est laissé à sa valeur par défaut) : un nœud qui a au moins cet espace libre est en dessous.
Classement : **infaisable** / **tendu** (devient infaisable si l'on perd un nœud ou une machine) / **OK**, avec l'explication textuelle de la contrainte limitante. C'est une heuristique : elle ne remplace pas `_cluster/allocation/explain`.

### 7.6 Pertes et disparitions

Sur une fenêtre [A, B], toute copie dont l'intervalle se ferme est classée :
- **supprimée** : l'index est supprimé dans la même fenêtre (filtre séparé) ;
- **déplacée** : une copie `(index, shard, p/r)` réapparaît sur un autre nœud et le nœud source est toujours présent ;
- **promue** : réplica devenu primaire ;
- **perdue** : son nœud a quitté le cluster dans la même fenêtre (ou la copie a disparu sans successeur).

Les pertes sont **regroupées par nœud parti** (puis par machine/rack si toute la machine est partie). Pour chaque copie perdue : avant (nœud, zone, p/r, taille) et après (réplica promu / non assigné avec raison / réalloué sur tel nœud à telle date, jamais avant la perte / **supprimée** : son index a été supprimé ensuite, avant que la copie soit reconstruite / toujours non assigné en B). La zone est la valeur du premier attribut d'awareness, lu dans les cluster settings ou à défaut dans la configuration des nœuds (defaults).

### 7.7 Chemin parcouru

Pour une copie de shard, un index ou une famille : la séquence des intervalles entre A et B (nœud, état, fenêtre), y compris les allers-retours invisibles dans un simple diff A/B. B est inclus : une position prise en B fait partie du chemin, comme de l'état en B. Au-delà de 20 000 intervalles, le chemin est coupé et l'indique (`truncated`) ; les premiers intervalles dans l'ordre (index, shard, p/r, début) sont gardés.

## 8. Interface web

### 8.1 Principes

- **Air-gapped** : tout embarqué (JS, CSS, polices sous licence OFL), aucune requête externe.
- **Langue FR/EN** et **thème clair/sombre** commutables instantanément (mémorisés dans le navigateur et le profil). **Heure locale** du navigateur ; UTC en infobulle.
- **Grandes cardinalités** : agrégation côté serveur (familles, top N + « autres »), rendu **canvas** pour les matrices et les grands ensembles, descente au shard seulement sur un périmètre filtré. Pagination/limites sur toutes les listes de l'API. **(v1.0)** Pas de tableaux virtualisés : la pagination côté serveur borne la taille du DOM de la même façon, et chaque page reste un permalien.
- **Animations utiles** : transitions entre deux dates (nœuds qui apparaissent/disparaissent et tuiles qui glissent dans la vue physique, cellules interpolées dans la matrice logique, couleurs qui changent), jamais bloquantes (environ 300 ms), désactivées si `prefers-reduced-motion`.
- **Permaliens** : cluster + date(s) + vue + filtres dans l'URL.
- Navigateurs : Chrome, Edge, Firefox récents.
- **(v1.1) Aide dans l'interface** : chaque écran a un encadré repliable « Comprendre cet écran » (choix mémorisé par écran dans le navigateur) ; les zones et colonnes ambiguës portent une icône « i » ; les vues colorées ont une légende ; les valeurs portent leur unité. Les infobulles natives sont remplacées par une bulle unique, en taille de texte normale, sur plusieurs lignes, affichée au survol et au focus clavier.
- **(v1.1) Date de référence** : toutes les vues datées (cluster, groupe, nœud, famille, index, contraintes) portent le même bandeau temporel ; la date est dans l'URL (`at`) et suit la navigation.
- **(v1.1) Rendu** : jetons de design uniques pour les deux thèmes (couleurs, trois niveaux d'ombre, rayons, durées) ; surfaces translucides sur un halo de fond fixe, dégradés légers sur les blocs, accent indigo → cyan ; flou d'arrière-plan réservé à l'en-tête, à la liste de recherche et à l'infobulle. Police **IBM Plex Sans** (OFL, embarquée) pour le texte, JetBrains Mono pour le code. Icônes dessinées en SVG en ligne, sans bibliothèque.
- **(v1.1) Transitions d'écran** : API View Transitions du navigateur, branchée dans le routeur. Descendre dans la hiérarchie (cluster → groupe → nœud, famille → index) fait un zoom avant, remonter un zoom arrière, rester au même niveau un fondu ; le lien cliqué devient le titre de la page d'arrivée. Un changement de date ou de filtre n'est jamais animé au niveau de la page. Seuls `transform` et `opacity` sont animés (120 ms au survol, 220 ms pour un écran) ; les clics passent pendant une transition ; tout est coupé si `prefers-reduced-motion`.

### 8.2 Écrans

1. **Accueil multi-clusters** : une ligne par cluster, triée par gravité ; frise de santé 7 jours, compteurs (nœuds / indices / shards / non assignés), versions présentes, derniers événements notables.
2. **Cluster à la date T** :
   - bandeau temporel : frise de santé + densité d'événements + curseur (le glisser met à jour la vue, rendu final au relâchement) ;
   - vue **physique** : DC → rack/machine → nœuds en tuiles, tiers en bandes, coloration au choix (non assignés par défaut, disque %, CPU process, heap, nb shards, version) ;
   - vue **logique** : familles × nœuds (matrice agrégée, canvas) ;
   - **(v1.1)** le panneau latéral de détail est supprimé. L'état du cluster, les nœuds manquants, les remotes CCS et les versions sont dans un bandeau au-dessus de la topologie ; l'infobulle d'un nœud donne machine, IP, rôles, version, attributs, shards, disque, CPU et heap ;
   - **(v1.1)** un clic sur un nœud ouvre sa page ; un clic sur le nom d'un cadre (zone, tier, machine) ouvre la page de ce groupe (écran 12). Il n'y a plus de sélection de nœud (`?sel=`) ;
   - **(v1.1)** cadres de tier teintés selon la température (hot, warm, cold, frozen, content, master).
   - **(v1.1) Vue logique**, réglages tous portés par l'URL :
     - lignes par **famille** ou par **index** (`rows=index` ; les indices les plus gros d'abord, puis les plus récents) ;
     - **filtre** texte : sur les noms de familles (les noms d'indices ne sont cherchés que si aucune famille ne correspond), ou sur les noms d'indices en mode index ;
     - nombre de lignes : 30, 60, 120 ou toutes (500 au plus), le reste dans une ligne « autres » ignorée par l'échelle de couleurs ;
     - colonnes : un nœud par colonne, ou regroupées par niveau de la hiérarchie (zone, tier dans chaque zone, machine) avec la somme écrite dans la case ;
     - dans tous les modes, les colonnes suivent la hiérarchie, séparées par des espaces (d'autant plus larges que le niveau est élevé) et surmontées d'un bandeau par niveau (zone, tier à la couleur de sa température, machine).
   - API : `GET /clusters/{id}/matrix?top=&q=&rows=` renvoie aussi le nombre total de lignes correspondantes et, en mode index, les UUID.
3. **Nœud** : générations, présence dans le temps, attributs et versions (historique), métriques (mini-graphes), contenu à T (shards par famille). **(v1.1)** Les métriques occupent les trois quarts de la largeur (graphiques de 150 px, 1 200 points, repères à 25/50/75 %). Présence puis attributs et versions sont dans le quart gauche : version, JVM, OS, rôles (teintés par tier) et attributs en pastilles ; les périodes précédentes sont repliées et ce qui a changé est entouré. Une génération nouvelle (même nom, nouvel identifiant ES, donc répertoire de données vidé ou remplacé) est expliquée sur place.
4. **Famille / index** : liste des indices de la famille, puis index → matrice shards × nœuds, settings et leur historique, **chemin des shards** (frise par copie, couleur = nœud, détail des intervalles), autres vies du même nom.
   - **(v1.1)** Famille : graphiques sur 7 jours des documents, de la taille sur disque et du nombre d'indices (§7.3) ; l'échelle des deux premiers part de la valeur la plus basse.
   - **(v1.1)** Index : la période du chemin des shards se choisit (dernière heure, 6 h, 24 h, 7 jours par défaut, 31 jours, ou deux dates) ; elle se termine à la date de référence et est ramenée à la vie de l'index (rien avant sa création ni après sa suppression). Sur cette même période, trois graphiques : documents, taille sur disque, taille des primaires (`GET /clusters/{id}/indices/{uuid}/history`).
5. **Diff A/B** : **(v1.1)** période par défaut : la dernière heure, pour cet écran comme pour les flux, les pertes et le journal ; raccourcis 1 h, 6 h, 24 h, 7 jours. Synthèse chiffrée (nœuds ±, indices ±, shards déplacés/perdus/nouveaux/promus, settings modifiés) ; onglets nœuds / indices / shards / settings / cluster settings / CCS. Les copies sont d'abord comparées nœud par nœud : un réplica devenu primaire sur son nœud est **promu**, et la copie disparue compte comme un réplica perdu (et non comme un primaire déplacé). Un nœud vu par une sonde mais pas encore décrit par une capture complète n'est pas « modifié ». Quand seules des sondes légères précèdent A, le diff part de la première capture complète ; **chemin parcouru** par défaut (événements de mouvement de la fenêtre) ; exports CSV, JSON, XLSX.
6. **Flux de déplacements** sur une période : Sankey DC/tier/machine/nœud → DC/tier/machine/nœud, histogramme des relocations dans le temps, clic sur un flux → liste des shards ; trajectoire d'un shard (frise de type Gantt).
7. **Pertes et disparitions** (§7.6) : groupes par nœud/machine perdu, avant/après, re-réplication, filtre « suppressions volontaires ». L'API plafonne chaque groupe (1 000 copies par défaut) ; la synthèse compte tout et l'export contient tout.
8. **Journal d'événements** : filtrable par type, gravité, nœud, machine, famille, période ; fenêtre d'incertitude ou date exacte affichée.
9. **Recherche globale** (omnibox, complétion) : nœud, IP, machine, nom ou UUID d'index, famille, « shard N de X à T » (aussi `shard N of X at T`, `X[N]`, `X[N] @ T`) → page d'historique de l'entité.
10. **Contraintes d'allocation** (§7.5) : liste triée infaisable → tendu → OK, explication, filtres.
11. **Administration** (§9).
12. **(v1.1) Groupe** (zone, tier ou machine), adresse `/c/<cluster>/g/<label>/<label>…` : cartes de synthèse (nœuds présents et manquants, machines, copies et primaires, disque, CPU et heap moyens et max, mouvements, versions), topologie du seul groupe en grandes tuiles nommées (tuiles simples au-delà de 48 nœuds), tableau triable des nœuds, contenu par famille, événements des nœuds et machines du groupe sur 7 jours (hors déplacements de shards), graphiques sur 7 jours des métriques de ses nœuds (CPU processus et heap en moyenne, disque utilisé, shards en somme, nœuds mesurés ; un nœud présent sans nouvelle mesure dans une tranche compte avec sa dernière valeur connue, à cause du deadband ; pour une machine, aussi CPU machine et load ; `GET /clusters/{id}/group/history`). API : `GET /clusters/{id}/group?g=…&at=` et filtre `g=` sur `/events`.
   - Les pages groupe et nœud portent un bouton **« Zoom arrière »** vers le niveau au-dessus et un fil d'Ariane cliquable (cluster / zone / tier / machine / nœud).

## 9. Administration

- **Clusters** : CA par défaut ; ajout/édition (nom, URL(s), type d'auth, chemins cert/clé/passphrase/API key/mot de passe, CA dédié éventuel, timeout des appels ; **(v1.2)** le délai de connexion, 10 s, n'est pas réglable), activation, **tester la connexion** (version, uuid, privilèges), **planification**, hiérarchie, regex de familles avec aperçu, référence de nœuds déclarée (optionnelle), acquittements (avec date d'effet), accepter une nouvelle identité ou rattacher comme nouveau cluster, purge.
- **Planification** (par cluster, dans le formulaire) : période de sonde (5 min), `min_interval` (10 min), `max_interval` (60 min), seuil de déclenchement, jitter ; validée côté serveur. Le parallélisme est global (onglet Collecteurs).
- **Rétention et stockage** : durée, taille max, partitions (taille, période), octets/jour par cluster, projection 30 j / 1 an.
- **Collecteurs** : parallélisme ; par cluster, dernière sonde et capture (statut, durée totale, **durée et taille par appel API**), erreurs récentes, prochaine exécution. Un bandeau signale un cluster dont l'historique est en avance sur l'horloge (§6.7). **(v1.2)** « Capturer maintenant » est dans le formulaire du cluster (onglet Clusters).
- **Settings** : liste blanche des settings d'index.
- **Authentification** : mode (aucune / local / LDAP), utilisateurs, configuration LDAP + test pas à pas.
- **HA** : rôle, primaire suivi ou standby surveillée, jeton configuré, retard de réplication par cluster, promouvoir, reconstruire, rétrograder, reprendre.
- **Système** : version, uptime, mémoire, nombre d'états gardés en cache, activation de `/metrics`, audit des actions admin. **(v1.2)** La taille des données est dans l'onglet Rétention et stockage.
- **(v1.0)** Les modifications de configuration sont enregistrées dans le journal d'audit. Sur une standby, l'admin est en lecture seule (§11).

## 10. Authentification et sécurité web

- Modes : **aucune** (tout le monde est admin, bandeau d'avertissement), **local**, **LDAP** (OpenLDAP en LDAPS avec la CA d'entreprise : compte de service, recherche de l'utilisateur, bind avec son mot de passe, rôle selon appartenance à un groupe admin / lecteur).
- Rôles : **lecteur** (tout consulter, exporter) ; **admin** (en plus : configuration, acquittements, capture forcée, purge, HA).
- Comptes locaux : mots de passe argon2id (m = 64 Mio, t = 3, p = 2), 12 caractères minimum. **(v1.2)** À la connexion, le nom d'un compte local est comparé sans tenir compte de la casse. Un utilisateur change son propre mot de passe depuis l'en-tête (clic sur son nom). Tant que l'authentification est activée, le dernier compte local admin actif ne peut être ni supprimé, ni désactivé, ni rétrogradé.
- **(v1.0) LDAP en détail** :
  - URL `ldaps://`, ou `ldap://` **avec StartTLS obligatoire** (bind simple en clair refusé) ; plusieurs URL essayées dans l'ordre ; CA en fichier PEM (sinon CA système) ;
  - mot de passe du compte de service dans un fichier, relu à chaque connexion ;
  - filtre utilisateur par défaut `(&(objectClass=inetOrgPerson)(uid={user}))`, nom échappé, exactement une entrée attendue ; **(v1.3)** le compte est nommé par l'attribut `user_attr` de l'entrée trouvée (`uid` par défaut, une seule valeur exigée), pas par le nom saisi : avec un filtre sur plusieurs attributs, une même personne n'a qu'un compte. Ce nom remplace `{user}` dans le filtre de groupe ;
  - groupes `groupOfNames`, `groupOfUniqueNames` et `posixGroup` par défaut (filtre réglable) ; DN comparés comme en LDAP ;
  - un mot de passe vide est refusé avant tout appel : ce serait un bind non authentifié, accepté par OpenLDAP ;
  - le rôle est relu à chaque connexion. Un compte désactivé par un admin le reste. Les noms sont ramenés en minuscules. **(v1.3)** Une connexion sans compte local coûte un calcul argon2id, comme un compte local : le temps de réponse ne révèle pas quels comptes locaux existent ;
  - annuaire injoignable → « annuaire indisponible » (503), non compté comme tentative échouée ;
  - les **comptes locaux restent utilisables en mode LDAP** : ce sont la voie de secours, avec `reset-password`. Passer en mode local ou LDAP exige donc un admin local ;
  - un test pas à pas dans l'admin vérifie la connexion, le compte de service, la recherche, le bind, les groupes et le rôle.
- Sessions par cookie `HttpOnly`, `Secure` en HTTPS, `SameSite=Strict` + jeton CSRF (en-tête `X-CSRF-Token`) sur les requêtes modifiantes. Seule l'empreinte SHA-256 du jeton est stockée. Expiration après 12 h d'inactivité par défaut, glissante. **(v1.2)** Ce délai se règle de 1 à 720 h ; l'échéance n'est repoussée que lorsqu'il reste moins de la moitié du délai ; la durée de vie absolue d'une session est de 7 jours. Les autres sessions d'un compte sont fermées au changement de mot de passe, au changement de rôle, à la désactivation et par `estopo admin reset-password`. **(v1.3)** Un changement de mode d'authentification ferme toutes les sessions, sauf celle de l'administrateur local qui le fait ; les vérifications du dernier admin et les écritures qu'elles protègent sont sérialisées. Toute requête modifiante doit porter `Content-Type: application/json` (avec un corps) ou `X-Requested-With: estopo` (sans corps) et venir de la même origine, dans tous les modes ; sinon elle est refusée (403 `cross_site`) : un proxy inverse doit conserver l'en-tête `Host`. Si les réglages d'authentification ou les sessions sont illisibles, la requête est refusée (503 `auth_unavailable`), jamais servie comme en mode « aucune ». Langue et thème mémorisés dans le profil.
- **(v1.0)** Limitation des tentatives : 5 échecs libres, puis attente de 2 s doublée à chaque échec, plafonnée à 15 min ; un succès efface le compteur. **(v1.2)** Les compteurs portent sur le couple utilisateur + adresse et sur l'adresse (5 échecs par 15 min), ainsi que sur l'utilisateur toutes adresses confondues (50 échecs par 15 min). **(v1.3)** Un succès n'efface que les compteurs du compte ; celui de l'adresse ne fait que lui rendre la tentative. Les noms sont comparés comme pour la recherche des comptes locaux (repliement de casse Unicode).
- HTTPS si `ESTOPO_TLS_CERT`/`ESTOPO_TLS_KEY` fournis (fichier avec chaîne complète accepté). **Les modes local et LDAP sont refusés en HTTP clair** (au démarrage comme au changement de mode), sauf écoute sur loopback ou `ESTOPO_ALLOW_INSECURE_AUTH=true` (bandeau d'avertissement, y compris sur l'écran de connexion). **(v1.2)** Quand estopo termine TLS, il envoie `Strict-Transport-Security`. Délais du serveur HTTP : lecture 30 s, écriture 10 min, inactivité 120 s.
- **(v1.2)** Premier démarrage : si `ESTOPO_ADMIN_PASSWORD` est défini alors qu'aucun mode d'authentification n'a été enregistré et qu'aucun compte n'existe, le compte local `admin` est créé avec ce mot de passe et le mode local est activé. Il faut 12 caractères au moins et un transport sûr (HTTPS, loopback ou `ESTOPO_ALLOW_INSECURE_AUTH`), sinon le démarrage est refusé. La variable est ignorée ensuite, ainsi que sur une instance démarrée avec `ESTOPO_STANDBY_OF`. Aucun mot de passe n'est plus généré dans les logs. Récupération : `estopo admin reset-password <user>`.

## 11. Haute disponibilité

- Une instance **primaire** collecte ; une **standby** passive ne contacte **jamais** les clusters ES.
- La standby tire du primaire (HTTPS + jeton partagé) : `meta.db` (instantané quand il change) puis les **records de capture** du spool, qu'elle ingère avec le même code (§6.7). Perte maximale lors d'une bascule : quelques minutes.
- **Reconstruction** d'une standby : copie des partitions scellées + instantané de la partition courante, puis rattrapage par les records. **(v1.2)** Tout est téléchargé et vérifié avant de remplacer les partitions locales : il faut jusqu'à 2× l'espace disque du cluster pendant l'opération.
- **Promotion manuelle** (bouton admin sur la standby). Pas de bascule automatique (évite le split-brain). L'ancien primaire, s'il revient, doit être rétrogradé à la main ; s'il voit une standby promue sur le pair configuré, il suspend sa collecte et l'affiche.
- La configuration étant dans `meta.db`, elle suit ; les **fichiers de certificats doivent exister aux mêmes chemins** sur la standby.
- **(v1.0) Mise en œuvre** :
  - **API de réplication** du primaire (`GET /api/v1/replication/...`, jeton `Bearer` d'au moins 16 caractères, comparé à temps constant ; sans jeton configuré, l'API répond 404). Elle expose l'état, la configuration (JSON déterministe + ETag), le catalogue des partitions, la plage du spool et le dernier record intégré par cluster, un instantané cohérent d'une partition (`VACUUM INTO`), la liste des records postérieurs à une date, et chaque record.
  - **Jeton** : le jeton ne circule jamais en clair. Côté primaire, l'API de réplication exige HTTPS, une écoute loopback ou `ESTOPO_ALLOW_INSECURE_AUTH`. Côté standby, l'URL du primaire doit être en `https://`, sauf vers loopback ou avec la même dérogation. **(v1.2)** `ESTOPO_REPLICATION_CA` (sinon CA système) sert à tous les appels de réplication : de la standby vers le primaire, et du primaire vers la standby qu'il surveille. Un fichier illisible ou sans certificat empêche le démarrage.
  - **Ce qui est répliqué** : la standby importe toutes les 30 s, dans une transaction, les tables de configuration (`cluster`, `setting`, `family_rule`, `user`, `node_ack`, `audit`). Les comptes LDAP créés localement sont gardés, sauf si l'audit répliqué montre leur suppression sur le primaire après leur création. **(v1.3)** Les sessions de la standby sont fermées comme sur le primaire : celles d'un compte dont le mot de passe, le rôle ou la source change, ou qui est désactivé ou supprimé, et toutes au changement de mode d'authentification. Les caches ne sont invalidés que si la configuration « topologique » change, pas à chaque connexion enregistrée dans l'audit. Les deux instances doivent avoir le même schéma de `meta.db` (même version d'estopo).
  - **Reconstruction par cluster**, automatique quand la standby n'a pas d'historique, ou quand elle est plus en retard que ce que le spool du primaire contient encore (72 h), y compris quand ce spool ne contient plus rien d'un cluster dont l'historique va plus loin. Elle est manuelle (« Reconstruire ») pour tous les clusters. Une reconstruction demandée (bouton, rétrogradation) est enregistrée dans `ha_state` : un redémarrage avant qu'elle soit faite ne l'annule pas, ni la fin d'une reconstruction déjà en cours. Un cluster purgé sur le primaire l'est aussi sur la standby : la purge supprime les partitions et les records du spool du cluster, qui ne réapparaît donc pas au démarrage suivant et n'est plus annoncé à la standby.
  - **(v1.2) Identifiant d'instance et blocage** : chaque dossier de données porte un identifiant d'instance aléatoire. La standby retient celui de son primaire à la première synchronisation. Si le primaire répond avec un autre identifiant, ou n'annonce aucun cluster alors que la standby possède de l'historique, la réplication est **bloquée** : rien n'est importé ni supprimé (champ `replication.blocked`, affiché dans Admin › Haute disponibilité ; métrique `estopo_replication_blocked`), jusqu'à une reconstruction explicite ou une promotion ; un primaire qui ne répond pas laisse le blocage en place. La promotion n'attend pas un primaire qui ne répond plus.
  - Une standby (ou un ancien primaire) est en **lecture seule** : toute modification est refusée (409), sauf les actions HA.
  - **Quarantaine** : le primaire surveille sa standby (URL saisie dans l'admin ; après une promotion, la nouvelle primaire surveille l'ancienne). Quand le pair a été promu **plus récemment**, l'instance s'arrête de collecter (`fenced`, bandeau rouge). La promotion la plus récente gagne, ce qui exclut toute quarantaine croisée. La vérification a lieu aussi au démarrage, **avant** de relancer la collecte.
  - **Actions** : sur une standby, promouvoir et reconstruire ; sur un ancien primaire, rétrograder (il redevient standby et recopie tout l'historique du nouveau primaire, ses records propres étant perdus) ou reprendre (refusé tant que le pair se dit primaire plus récent).
  - `ESTOPO_STANDBY_OF` ne sert qu'au premier démarrage : ensuite, l'état local (`ha_state`) l'emporte.

> Choix de conception : copier en continu la partition de la semaine en cours (qui change toutes les 5 min) coûterait plusieurs Go d'IO par heure ; faire transiter les records est plus léger pour le même résultat.

## 12. Exploitation

### 12.1 Variables d'environnement (uniquement ce qu'il faut avant de lire la base)

| Variable | Défaut | Rôle |
|---|---|---|
| `ESTOPO_LISTEN` | `127.0.0.1:8080` (v1.2) | adresse et port d'écoute ; exposer le service demande de la définir |
| `ESTOPO_DATA_DIR` | `./data` (à côté du binaire) | dossier de données |
| `ESTOPO_TLS_CERT` / `ESTOPO_TLS_KEY` | — | HTTPS de l'UI (cert avec chaîne complète accepté) |
| `ESTOPO_ADMIN_PASSWORD` | — | (v1.2) au tout premier démarrage (aucun réglage d'authentification, aucun compte) : crée le compte local `admin` et active le mode local (§10) ; ignorée ensuite |
| `ESTOPO_ALLOW_INSECURE_AUTH` | `false` | autorise l'authentification, et le jeton de réplication, en HTTP clair |
| `ESTOPO_LOG_LEVEL` / `ESTOPO_LOG_FORMAT` | `info` / `text` | journaux (`json` possible) |
| `ESTOPO_REPLICATION_TOKEN_FILE` | — | fichier du jeton de réplication partagé (§11) : active l'API de réplication d'un primaire, obligatoire sur une standby |
| `ESTOPO_STANDBY_OF` | — | URL du primaire : premier démarrage en standby (exige le jeton) |
| `ESTOPO_REPLICATION_CA` | CA système | (v1.2) CA du certificat de l'autre instance, sur la standby comme sur le primaire ; illisible ou vide : démarrage refusé |
| `ESTOPO_PPROF_LISTEN` | — | (v1.0) adresse **loopback** du profileur Go (diagnostic), désactivé par défaut |
| `ESTOPO_SECRETS_DIR` | — | (v1.2) quand elle est définie, tout chemin de fichier saisi dans l'admin (clusters, CA par défaut, LDAP) doit se trouver dans ce dossier |

Tout le reste se règle dans l'admin.

### 12.2 Exemple de service systemd (fourni dans `deploy/`, pas installé automatiquement)

```ini
# /etc/systemd/system/estopo.service
[Unit]
Description=ES.Topo - Elasticsearch topology history
After=network-online.target
Wants=network-online.target

[Service]
User=estopo
Group=estopo
EnvironmentFile=/etc/estopo/estopo.env
ExecStart=/opt/estopo/estopo serve
Restart=on-failure
LimitNOFILE=65536
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
ReadWritePaths=/var/lib/estopo

[Install]
WantedBy=multi-user.target
```

```sh
# /etc/estopo/estopo.env
ESTOPO_LISTEN=0.0.0.0:8443
ESTOPO_DATA_DIR=/var/lib/estopo
ESTOPO_TLS_CERT=/etc/estopo/tls/estopo-fullchain.pem
ESTOPO_TLS_KEY=/etc/estopo/tls/estopo.key
ESTOPO_LOG_FORMAT=json
ESTOPO_SECRETS_DIR=/etc/estopo/secrets
```

**(v1.2)** Les fichiers de `deploy/` font foi ; l'en-tête de l'unité donne les commandes d'installation (utilisateur système, dossiers `/opt/estopo`, `/etc/estopo` et `/var/lib/estopo`).

### 12.3 Supervision

`/healthz` (vivant + état de la collecte + rôle HA) ; `/metrics` au format Prometheus (optionnel, désactivé par défaut) ; journaux sur stdout (journald).

**(v1.0)** `/metrics` s'active dans Admin › Système. Il est servi sans authentification, comme la plupart des cibles Prometheus, et n'expose aucune donnée de topologie : dates, durées et succès des sondes et captures, suspension par cluster, ingestion (records, erreurs, durées, file d'attente), taille de l'historique par cluster, rôle HA, retard de réplication, mémoire Go. **(v1.2)** S'y ajoutent `estopo_ingest_skipped_total` (records abandonnés après des échecs d'ingestion répétés) et `estopo_replication_blocked`.

## 13. Simulateur et tests

### 13.1 Simulateur `essim`

- Serveur HTTP(S) Go imitant les API de §5 pour **plusieurs clusters simulés**, avec variantes de format 7.17 / 8.x / 9.x.
- Topologie paramétrable, jeu de référence : cluster A ~200 nœuds, 3 DC, hot/warm/cold, 4 nœuds par machine hot et 8 par machine warm/cold, attributs d'awareness DC + machine, indices journaliers sur 90 jours pour N sources (~250 000 shards) ; cluster B ~100 000 shards ; cluster C cold peu actif.
- **Horloge accélérée** et scénarios : rollover quotidien et suppression par rétention, rééquilibrage, perte d'un nœud / d'une machine / d'un rack / d'un DC puis retour, remplacement d'un nœud (nouvelle génération), upgrade progressif (versions mixtes), changements de settings d'index et de cluster, blocage par `total_shards_per_node`, déconnexion CCS, lenteurs / timeouts / 503 / pas de master, changement de `cluster_uuid`.
- **mTLS** avec une PKI de test générée (racine + intermédiaire, comme en entreprise).
- Mode **backfill** : collecteur et simulateur partagent une horloge accélérée pour produire un historique bien plus vite qu'en temps réel (tests de perf, données de démonstration). **(v1.2)** Ordre de grandeur : environ une demi-heure pour le jeu de démonstration de 14 jours (`essim backfill`), quelques heures pour le jeu de référence de 30 jours.

### 13.2 Tests

- Unitaires : parsing (fixtures 7.17/8/9), ingestion par intervalles, événements, gravité, familles, contraintes, pertes, rétention.
- Scénarios de bout en bout contre `essim` (comportements de §15).
- Validation des formats contre de **vrais ES 7.17, 8.x, 9.x** en conteneurs Docker (`scripts/capture-fixtures.sh` : cluster de 4 nœuds sécurisé, PKI racine → intermédiaire, realms PKI + native + file, licence d'essai ; le client Go est testé avec les trois authentifications).
- **(v1.0)** LDAP : faux serveur LDAPS en Go pour les tests unitaires ; vrai OpenLDAP (paquet Debian) en LDAPS et StartTLS par `testenv/ldap/run.sh`.
- Tests UI de bout en bout (Playwright, en développement uniquement).
- Test de démarrage du binaire dans des conteneurs Rocky/Alma 8, 9, 10 (`testenv/distros/run.sh` : HTTPS, utilisateur non privilégié, sans réseau).

## 14. Performances cibles (sur le jeu de référence, 30 jours backfillés, PC de développement)

| Mesure | Cible | **(v1.0) Mesuré** (cluster A, 245 000 copies, p95) |
|---|---|---|
| Ingestion d'une capture complète de 250 k shards (hors temps de réponse ES) | < 30 s | 0,82 s en moyenne, 5,3 s au maximum |
| Mémoire résidente, 15 clusters en collecte | ~~< 1,5 Go~~ indicative (v1.0) | pic 1,41 Gio, 0,93 Gio en régime établi |
| Synthèse d'un cluster à T (cache chaud / froid) | < 300 ms / < 2 s (p95) | 54 ms / 1,0 à 1,6 s |
| Détail d'un nœud | < 300 ms | 5 ms |
| Page de shards filtrée | < 500 ms | 10 ms |
| Diff A/B sur 7 jours (synthèse) | < 3 s | 2,3 à 2,8 s |
| Pertes sur 24 h | < 2 s | 1,35 s |
| Complétion de la recherche | < 150 ms | 2 ms |
| Taille disque des 30 jours | mesurée et ≤ 2× l'estimation de §3, sinon révision du modèle | 1,015 Go, environ 1× |

- **(v1.0)** « Cache chaud » = date déjà consultée. Passer à une date encore jamais vue coûte la reconstruction de l'état (environ 0,6 s pour A).
- **(v1.0)** La cible de 1,5 Go est indicative : la taille du cache des états est réglable dans l'administration.
- Les états reconstruits sont mis en cache : LRU de **64 états** par défaut (réglable, environ 20 Mo par état d'un cluster de 250 000 shards). La synthèse de l'accueil est elle aussi en cache, recalculée à chaque nouvelle capture (accueil à 15 clusters : 3 ms à chaud). L'état courant de chaque cluster reste en mémoire.
- **(v1.2)** Ordre de grandeur à retenir, mesuré sur données simulées : environ 1 Go pour 30 jours d'historique de 373 000 copies de shards.

## 15. Comportements attendus (cas vérifiables)

| # | Entrée | Résultat attendu |
|---|---|---|
| B1 | Deux captures complètes identiques | Aucun nouvel intervalle ; seules `capture`, `health` et les métriques hors zone morte sont écrites. |
| B2 | Un nœud disparaît entre deux sondes (5 min) | Événement `node_left` avec fenêtre ≤ 5 min ; capture complète déclenchée (sous réserve de `min_interval`) ; nœud marqué manquant (gravité 2). |
| B3 | Une machine (8 nœuds) disparaît | Un événement `machine_lost` regroupant les 8 nœuds ; l'écran Pertes groupe les copies perdues par machine puis par nœud. |
| B4 | Copie primaire perdue, réplica présent ailleurs | Classée *perdue* ; le réplica est classé *promu* ; l'après indique la nouvelle copie réallouée avec sa date. |
| B5 | Index supprimé volontairement | Copies classées *supprimées*, absentes de l'écran Pertes par défaut. |
| B6 | Shard déplacé N1 → N2 → N1 entre A et B | Le diff A/B ne montre rien ; le **chemin** montre les deux déplacements avec leurs fenêtres. |
| B7 | `_cat/shards` en timeout | Capture `partial`/`failed`, aucun intervalle fermé, bande « inconnu » dans l'UI, événement `capture_failed`. |
| B8 | Changement de `cluster_uuid` sur une URL | Collecte suspendue, alerte admin, rien n'est ingéré. |
| B9 | Même nom de nœud, nouvel id ES | Événement `node_new_generation`, génération +1, historique continu sous le même nom. |
| B10 | Nœud retiré durablement puis acquitté | Il n'est plus compté manquant à partir de la date d'acquittement ; action tracée dans l'audit. |
| B11 | Index recréé sous le même nom | Deux entités distinctes (UUID) ; la recherche par nom montre les deux vies. |
| B12 | `total_shards_per_node` trop bas / nœuds éligibles | Index classé *infaisable* avec explication ; *tendu* si la perte d'une machine le rendrait infaisable. |
| B13 | Rétention 30 j dépassée | Les partitions entièrement hors rétention sont supprimées ; les autres restent lisibles seules (keyframe). |
| B14 | Taille max dépassée | Suppression des plus anciennes partitions jusqu'au seuil ; événement et bandeau admin. |
| B15 | Agitation continue (relocations en rafale) | Pas plus d'une capture complète par `min_interval`. |
| B16 | Tentative d'appel non-GET dans le client ES | Refusée par le client (test unitaire). |
| B17 | Mode local sur HTTP clair, écoute non loopback | Démarrage refusé avec message explicite, sauf `ESTOPO_ALLOW_INSECURE_AUTH=true`. |
| B18 | Standby, primaire arrêté, promotion | La standby commence à collecter ; l'historique contient toutes les captures jusqu'au dernier record reçu. |
| B19 | Upgrade progressif 8.x → 9.x | Événements `node_upgraded` ; l'accueil et la vue cluster montrent les versions mixtes. |
| B20 | Changement de cluster setting (ex. `allocation.enable=primaries`) | Événement `cluster_settings_changed`, diff clé par clé affiché. |
| B21 | Remote CCS déconnecté | Événement `remote_disconnected` ; visible dans la vue cluster et le diff. |
| B22 | Export XLSX d'un diff | Fichier ouvrable, un onglet par catégorie, colonnes et libellés dans la langue de l'UI. |

## 16. Critères d'acceptation

1. `go test ./...` passe (unitaires + scénarios contre `essim`).
2. `CGO_ENABLED=0 go build` produit `estopo` et `essim` pour linux/amd64, windows/amd64 et, **(v1.2)**, darwin/arm64 ; `estopo` démarre et répond sur `/healthz` dans des conteneurs Rocky/Alma 8, 9 et 10.
3. Tests de format passants contre de vrais ES 7.17, 8.x et 9.x.
4. Backfill de 30 jours du jeu de référence : cibles de §14 mesurées et rapportées (tableau de §14).
5. B1–B22 couverts par des tests automatisés (UI via Playwright pour les écrans concernés).
6. UI utilisable sans aucun accès réseau externe (vérifié : aucune requête hors origine dans Playwright).
7. README EN/FR, SPEC EN/FR, exemple systemd et `build-release.sh` à jour.

## 17. Étapes de réalisation

**(v1.0)** La réalisation a réordonné les étapes : formats réels ES dès le début, client avant simulateur, backfill et bench avant l'UI. Ordre initialement proposé :

1. **Squelette** : dépôt git local, modules, arborescence, configuration par env, logs, `build-release.sh`, licence AGPL-3.
2. **Simulateur** `essim` : topologie, formats 7.17/8/9, horloge accélérée, scénarios, PKI de test.
3. **Client ES et collecte** : TLS/mTLS/API key/basic, lecture seule, flux JSON, sonde légère, capture complète, déclencheurs, records et spool.
4. **Stockage et ingestion** : `meta.db`, partitions, intervalles, keyframes, zone morte, blobs, événements, rétention/taille max.
5. **Requêtes et API** : état à T (cache), diff, chemin, pertes, journal, recherche, agrégations, exports CSV/JSON/XLSX.
6. **Backfill et bench** : jeu de référence 30 jours, mesures de §14, ajustements.
7. **UI (1)** : socle (i18n, thème, navigation, permaliens), accueil, vue cluster + bandeau temporel, nœud, famille/index.
8. **UI (2)** : diff et chemin, pertes, journal, recherche, flux (Sankey, trajectoires), contraintes d'allocation.
9. **Admin et authentification locale**, HTTPS, sessions, audit.
10. **LDAP**.
11. **HA** : réplication par records, reconstruction, promotion.
12. **Finition** : validation contre vrais ES, tests de démarrage RHEL-like, documentation EN/FR, release.

Chaque étape se termine par une vérification exécutable (tests, build, bench) avant la suivante.

## 18. Questions ouvertes et points à vérifier

- **Coût réel** de `_cat/shards` et de `_all/_settings/<liste>` sur un cluster de 250 k shards réel : à mesurer via la page Collecteurs. Plan B si trop coûteux : ne lire les settings que des indices nouveaux ou dont `settings_version` a changé (`_cluster/state/metadata` filtré).
- `fs.io_stats` : présent mais vide dans les conteneurs de test, donc non vérifié ; à confirmer sur un cluster réel (page Collecteurs). `os.cgroup` est présent (quota `-1` sans limite).
- **(v1.0, vérifié)** `http.compression` : aucune réponse gzip sous TLS avec les réglages par défaut, sur les trois versions. Le client demande le gzip et sait le décoder, sans en dépendre.
- **(v1.0, vérifié)** Realm PKI : fonctionne avec la licence d'essai dans les conteneurs de test. La licence requise en production n'est **pas vérifiée**.
- **(v1.0, vérifié)** Privilèges minimaux (§5.6) confirmés sur 7.17, 8.19 et 9.5.
- **(v1.0)** Rejeu manuel du spool après correction d'un bug d'ingestion (§6.7) : reporté, à rediscuter.
- **(v1.0)** HA : la durée de copie d'une grosse partition et le fonctionnement entre deux machines en HTTPS réel restent à mesurer (testés en processus, sur HTTP loopback et en TLS unitaire).
- Tier frozen / searchable snapshots : tailles non significatives, à traiter le moment venu.
- Données de démonstration : `essim backfill` et le scénario `demo` (§13).
- Idée pour plus tard, hors périmètre : simulation « que se passe-t-il si je perds la machine / le rack X ? » à partir des contraintes de §7.5.
