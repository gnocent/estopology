*(English version: [high-availability.md](high-availability.md))*

# Haute disponibilité (guide avancé)

Ce guide explique comment faire tourner deux instances d'ES.Topo, l'une qui collecte et l'autre qui la suit, et comment passer de l'une à l'autre. Il suppose que vous savez installer une instance en HTTPS avec l'authentification activée (voir [Installation](installation_fr.md) et [Authentification](authentication_fr.md)).

Dans les exemples, les deux machines s'appellent `estopo-a.example.org` (le primaire au départ) et `estopo-b.example.org` (la standby au départ), et écoutent sur le port 8443.

## Ce que fait la paire, et ce qu'elle ne fait pas

- Le **primaire** collecte les clusters Elasticsearch et accepte les modifications de configuration.
- La **standby** ne contacte **jamais** les clusters. Toutes les 30 secondes, elle va chercher sur le primaire la configuration et les relevés nouveaux, et construit le même historique. Elle est en consultation seule.
- La **promotion est manuelle** : un administrateur clique sur un bouton de la standby. Il n'y a **pas de bascule automatique**, et ES.Topo ne fournit pas d'adresse commune aux deux instances : les utilisateurs ouvrent l'adresse de l'instance voulue, ou vous placez devant un mécanisme de votre choix.
- Un ancien primaire qui revient après une promotion s'en aperçoit, arrête de collecter (il se met en **quarantaine**) et attend qu'un administrateur le rétrograde. Cela ne fonctionne que s'il sait où se trouve l'autre instance : voir [Limites et risques](#limites-et-risques).
- La standby n'est **pas une sauvegarde** : ce qui est supprimé sur le primaire l'est aussi sur la standby.

## Prérequis

| Prérequis | Pourquoi |
|---|---|
| **Même version** d'ES.Topo sur les deux machines | La standby refuse de suivre un primaire dont les bases n'ont pas la même structure. |
| **HTTPS sur les deux instances** | Le jeton de réplication ne doit pas circuler en clair. La standby appelle le primaire, et le primaire appelle la standby pour la surveiller. |
| Chaque machine peut joindre l'autre sur son port | Dans les deux sens. |
| Un **fichier de jeton** identique sur les deux machines, de 16 caractères au moins | C'est le secret partagé qui autorise la réplication. |
| La **CA** qui a signé le certificat de l'autre instance, **sur les deux machines**, si ce n'est pas une CA connue du système | Variable `ESTOPO_REPLICATION_CA`. Si le fichier est illisible ou ne contient aucun certificat, le démarrage est refusé. |
| Les **mêmes fichiers de secrets aux mêmes chemins** sur les deux machines (certificats, clés, fichiers de mot de passe des clusters, CA, fichiers LDAP) | La configuration est répliquée, pas les fichiers. Après une promotion, la standby doit pouvoir lire les mêmes chemins. |
| Des **horloges à l'heure** (NTP) sur les deux machines | La quarantaine compare les dates de promotion des deux instances. |
| Sur la standby, autant d'espace disque que sur le primaire, plus une marge | Voir [Reconstruire](#reconstruire). |

## Mise en place

### 1. Le jeton

Sur `estopo-a`, créez le jeton, puis copiez le même fichier sur `estopo-b` par un canal sûr :

```bash
openssl rand -hex 32 > /etc/estopo/replication.token
chown root:estopo /etc/estopo/replication.token
chmod 0640 /etc/estopo/replication.token
```

Quiconque détient ce jeton peut lire toute la configuration et tout l'historique d'une instance, y compris les empreintes des mots de passe : protégez-le comme une clé privée.

### 2. Le primaire

Dans `/etc/estopo/estopo.env` de `estopo-a`, ajoutez :

```sh
ESTOPO_REPLICATION_TOKEN_FILE=/etc/estopo/replication.token
# Seulement si le certificat de estopo-b n'est pas signé par une CA du système :
ESTOPO_REPLICATION_CA=/etc/estopo/tls/ca.pem
```

Redémarrez (`systemctl restart estopo`). Vérification : dans **Administration › Haute disponibilité**, la ligne « Jeton de réplication » indique « oui » et le « Rôle » est « Primaire ».

### 3. La standby

Installez ES.Topo sur `estopo-b` comme une instance normale (binaire, service, certificat HTTPS), avec un dossier de données **vide**, et copiez-y les fichiers de secrets aux mêmes chemins. Dans son fichier d'environnement :

```sh
ESTOPO_LISTEN=0.0.0.0:8443
ESTOPO_DATA_DIR=/var/lib/estopo
ESTOPO_TLS_CERT=/etc/estopo/tls/estopo-fullchain.pem
ESTOPO_TLS_KEY=/etc/estopo/tls/estopo.key
ESTOPO_SECRETS_DIR=/etc/estopo/secrets
ESTOPO_REPLICATION_TOKEN_FILE=/etc/estopo/replication.token
ESTOPO_REPLICATION_CA=/etc/estopo/tls/ca.pem
ESTOPO_STANDBY_OF=https://estopo-a.example.org:8443
```

Ne définissez pas `ESTOPO_ADMIN_PASSWORD` : une standby reçoit ses comptes et son mode d'authentification du primaire.

Démarrez le service. La standby copie d'abord tout l'historique du primaire, puis suit les nouveaux relevés. Dans son journal : `following primary instance_id=…`, puis `partitions copied from the primary` pour chaque cluster.

`ESTOPO_STANDBY_OF` ne sert qu'au **premier** démarrage : le rôle est ensuite enregistré dans le dossier de données et l'emporte sur la variable. Une standby promue reste donc primaire même si la variable est toujours là (un avertissement le signale dans le journal).

### 4. Déclarer la standby sur le primaire

Sur `estopo-a`, dans **Administration › Haute disponibilité**, section « Standby surveillée » : saisissez `https://estopo-b.example.org:8443` et cliquez sur « Enregistrer ».

Résultat attendu, sous le champ : « Standby vue en rôle Standby le … ». Si la ligne affiche « Standby injoignable : … », le texte donne la raison : nom ou port erroné, pare-feu, certificat refusé (`ESTOPO_REPLICATION_CA` sur le primaire), jeton différent (`invalid replication token`).

**Cette étape n'est pas facultative.** C'est grâce à elle que le primaire s'aperçoit que la standby a été promue et cesse de collecter.

### 5. Vérifier

Connectez-vous sur `estopo-b` avec un compte du primaire (les comptes arrivent à la première synchronisation). Vous devez voir :

- le bandeau « Instance standby : consultation seule, la configuration et l'historique viennent du primaire. » ;
- les mêmes clusters et le même historique que sur le primaire ;
- dans **Administration › Haute disponibilité** : « Rôle : Standby », le « Primaire suivi », une « Dernière synchronisation » récente et un « Retard » faible.

## Ce qui est répliqué

Toutes les 30 secondes, la standby :

1. vérifie que le primaire est bien l'instance qu'elle suit (voir [Réplication bloquée](#réplication-bloquée)) ;
2. recopie la configuration si elle a changé : clusters, réglages (rétention, liste blanche, authentification et LDAP, CA par défaut…), règles de familles, comptes, acquittements, journal d'audit ;
3. pour chaque cluster, récupère les relevés qu'elle n'a pas encore et les intègre exactement comme le fait le primaire ;
4. recopie les fichiers d'historique complets d'un cluster quand elle n'a rien pour lui, ou quand elle est en retard de plus de 72 heures (la durée pendant laquelle le primaire garde les relevés bruts), ou en retard sur un primaire qui n'a plus les relevés bruts de ce cluster (par exemple un cluster désactivé pendant que la standby était arrêtée) ;
5. supprime l'historique d'un cluster que le primaire a purgé, avec ses relevés bruts.

Ne sont **pas** répliqués, parce qu'ils décrivent l'instance elle-même : les sessions (on se connecte séparément à chaque instance ; la standby ferme les siennes quand un mot de passe, un rôle ou le mode d'authentification change sur le primaire, ou quand un compte y est désactivé ou supprimé), les alertes, le rôle et l'état de haute disponibilité. Les fichiers de secrets et les variables d'environnement ne le sont pas non plus.

Une standby (comme un ancien primaire en quarantaine) refuse toute modification : clusters, comptes, réglages, test de connexion, test LDAP, changement de mot de passe. Le message est `this instance is standby: changes are made on the primary`. Seules les actions de haute disponibilité, la connexion et la déconnexion restent possibles.

## Lire la page Haute disponibilité

**Administration › Haute disponibilité**, sur n'importe quelle instance :

| Élément | Signification |
|---|---|
| « Rôle » | « Primaire », « Standby » ou « Ancien primaire (collecte suspendue) ». Après une promotion : « promue le … ». |
| « Primaire suivi » | Sur une standby : l'URL du primaire. |
| « Jeton de réplication » | « oui », ou « non configuré (ESTOPO_REPLICATION_TOKEN_FILE) ». |
| « Motif » | Sur un ancien primaire : pourquoi il s'est mis en quarantaine. |

Sur une standby, section « Réplication » :

| Élément | Signification |
|---|---|
| « Dernière synchronisation » | Dernier cycle terminé sans erreur. |
| « Retard » | Écart entre le relevé le plus récent du primaire (brut ou déjà dans son historique) et le plus récent intégré ici (le pire des clusters). |
| « Copie des partitions en cours… » | Une reconstruction est en cours. |
| « Dernière erreur » | Erreur du dernier cycle, avec sa date. |
| « Réplication bloquée : … » | Voir [Réplication bloquée](#réplication-bloquée). |
| Tableau par cluster | « Dernier record reçu », « Dernier record du primaire », « Records reçus » et « Copies de partitions » depuis le démarrage. |
| Boutons | « Promouvoir en primaire », « Reconstruire ». |

Sur un primaire ou un ancien primaire, section « Standby surveillée » : l'URL de l'autre instance, le résultat de la dernière vérification, et les boutons « Rétrograder en standby » et, en quarantaine, « Reprendre la collecte ».

## Procédures

### Bascule planifiée

Pour faire de `estopo-b` le primaire, par exemple avant une maintenance de `estopo-a` :

1. Sur `estopo-b`, vérifiez que le « Retard » est faible et qu'il n'y a ni erreur ni blocage.
2. Arrêtez `estopo-a` (`systemctl stop estopo`). Attendez une minute, le temps que la standby ait récupéré les derniers relevés.
3. Sur `estopo-b`, cliquez sur « Promouvoir en primaire » et confirmez. Le bandeau standby disparaît, le rôle devient « Primaire (promue le …) » et la collecte démarre : vérifiez-la dans **Administration › Collecteurs**.
4. Quand `estopo-a` redémarre, il interroge `estopo-b`, constate la promotion et se met en quarantaine (bandeau rouge). Cliquez alors, sur `estopo-a`, sur « Rétrograder en standby » (voir plus bas).

### Primaire perdu : promouvoir

1. Assurez-vous que le primaire est bien arrêté ou hors d'état de collecter.
2. Sur la standby, **Administration › Haute disponibilité**, « Promouvoir en primaire », puis confirmez.

La standby devient primaire avec tout ce qu'elle avait reçu. Ce que l'ancien primaire avait relevé sans avoir eu le temps de le transmettre (au plus quelques dizaines de secondes en fonctionnement normal) est absent du nouvel historique. La promotion n'attend pas un primaire qui ne répond plus. L'ancien primaire devient automatiquement l'instance surveillée par le nouveau.

### Retour de l'ancien primaire

Au démarrage, puis toutes les 30 secondes, un primaire interroge l'instance qu'il surveille. S'il voit qu'elle est primaire et qu'elle a été promue plus récemment que lui, il :

- arrête de collecter ;
- passe au rôle « Ancien primaire (collecte suspendue) » ;
- affiche le bandeau « Collecte suspendue : une standby a été promue depuis. Rétrograder cette instance (Administration › Haute disponibilité). » ;
- refuse toute modification.

Pour en refaire une standby : sur cet ancien primaire, **Administration › Haute disponibilité**, vérifiez l'« URL du nouveau primaire » (par défaut, l'instance surveillée), cliquez sur « Rétrograder en standby » et confirmez. L'instance recopie alors **tout** l'historique du nouveau primaire. Les relevés qu'elle était seule à posséder sont perdus. Si elle redémarre avant la fin de la copie, la copie est faite après le redémarrage.

### Reconstruire

« Reconstruire », sur une standby, recopie tout l'historique depuis le primaire. Utilisez-le quand la standby vous semble incohérente, ou pour sortir d'un blocage (section suivante). La demande survit à un redémarrage de la standby jusqu'à ce que la copie ait réussi.

La standby télécharge d'abord tous les fichiers d'un cluster, les vérifie, puis remplace les siens : pendant l'opération, il faut jusqu'à **deux fois** l'espace disque de ce cluster. Côté primaire, chaque fichier est d'abord copié dans le sous-dossier `tmp` du dossier de données avant d'être envoyé : prévoyez la place d'un fichier hebdomadaire supplémentaire. La page affiche « Copie des partitions en cours… ».

### Reprendre la collecte

« Reprendre la collecte » remet en service un ancien primaire en quarantaine **sans** le rétrograder. N'utilisez ce bouton que si l'autre instance n'est plus primaire (par exemple, elle a elle-même été rétrogradée ou détruite). Il est refusé tant que l'autre instance répond et se dit primaire plus récent ; il est accepté si elle ne répond pas : à vous de vous assurer qu'elle ne collecte plus.

### Remplacer une instance perdue

Si une machine est définitivement perdue, installez une nouvelle standby à partir d'un dossier de données vide (étape 3), avec `ESTOPO_STANDBY_OF` qui désigne le primaire actuel. Puis, sur le primaire, mettez à jour « Standby surveillée » si l'adresse a changé.

## Réplication bloquée

Chaque dossier de données porte un identifiant d'instance aléatoire, créé avec lui. À sa première synchronisation, la standby retient l'identifiant de son primaire. Elle **bloque** ensuite la réplication, sans rien importer ni rien supprimer, dans deux cas :

- le primaire répond avec un **autre identifiant** : son dossier de données a été remplacé (machine réinstallée, volume perdu, adresse qui pointe vers une autre instance) ;
- le primaire n'annonce **aucun cluster** alors que la standby possède de l'historique.

Sans ce garde-fou, un primaire revenu vide viderait la standby en un cycle.

Vous le voyez :

- dans **Administration › Haute disponibilité** de la standby : encadré « Réplication bloquée : » suivi de la raison, et « Rien n'est importé ni supprimé tant que le blocage dure. … » ;
- dans le journal de la standby : `replication stopped until an administrator rebuilds this standby` ;
- par la métrique `estopo_replication_blocked`, qui vaut 1.

Le blocage demeure tant que le primaire ne répond pas : seul un primaire qui passe la vérification le lève.

Pour en sortir, décidez quelle instance détient le bon historique :

| Situation | Action |
|---|---|
| Le primaire a perdu ses données et **la standby a le bon historique** | Sur la standby : « Promouvoir en primaire ». Puis refaites de l'autre machine une standby (« Rétrograder en standby » sur celle-ci, ou réinstallation avec `ESTOPO_STANDBY_OF`). |
| Le primaire a été remplacé ou vidé **volontairement**, et la standby doit devenir sa copie | Sur la standby : « Reconstruire ». Elle accepte le primaire tel qu'il est : si le primaire est vide, la standby est vidée. |
| L'adresse du primaire désigne par erreur une autre instance | Corrigez la résolution de nom ou le routage ; le blocage se lève au cycle suivant. |

## Limites et risques

- **Pas de bascule automatique.** Si le primaire tombe, plus rien n'est collecté tant que personne ne promeut la standby.
- **Sans « Standby surveillée » renseignée sur le primaire, il n'y a pas de quarantaine.** Un ancien primaire qui revient après une promotion reprend la collecte : deux instances collectent alors en parallèle et leurs historiques divergent. La surveillance exige aussi le jeton sur le primaire. Si cela arrive : sur l'ancien primaire, cliquez sur « Rétrograder en standby » en saisissant l'URL du nouveau primaire.
- **Une perte partielle sur le primaire se propage.** Le blocage ne se déclenche que si le primaire a changé d'identifiant ou n'annonce plus aucun cluster. Un cluster purgé, une rétention raccourcie ou une modification erronée de la configuration sont répliqués normalement. Faites des sauvegardes (voir [Exploitation](operations_fr.md#sauvegarde-et-restauration)).
- **Rétrograder fait perdre** les relevés que l'instance était seule à posséder.
- **Espace disque** : jusqu'à deux fois la taille d'un cluster pendant sa reconstruction.
- **Le jeton donne un accès complet en lecture**, empreintes de mots de passe comprises.
- **Si le primaire est sans authentification, la standby l'est aussi**, et tout visiteur peut la promouvoir. Activez l'authentification avant de mettre en place la paire.
- La réplication est couverte par des tests automatisés qui font tourner deux instances sur une même machine ; la durée de copie de très gros fichiers entre deux machines réelles n'a pas été mesurée.

## Superviser

Activez `/metrics` (voir [Exploitation](operations_fr.md#metrics)) et surveillez, sur la standby :

| Métrique | Alerte suggérée |
|---|---|
| `estopo_replication_blocked` | vaut 1 : intervention nécessaire |
| `estopo_replication_lag_seconds` | durablement élevé (par exemple plus de 900) |
| `estopo_replication_last_sync_timestamp_seconds` | trop ancien : la standby n'arrive plus à synchroniser |
| `estopo_ha_role{role="…"}` | sur chaque instance : un rôle inattendu (`fenced`, ou deux `primary`) |

`/healthz` donne aussi le rôle de chaque instance.

## Mettre à jour les deux instances

Les deux instances doivent tourner avec la même version. Procédez dans cet ordre :

1. arrêtez la standby ;
2. arrêtez le primaire, remplacez son binaire, redémarrez-le ;
3. remplacez le binaire de la standby, redémarrez-la.

Pendant un écart de versions, la standby refuse de synchroniser et l'indique dans « Dernière erreur » (`run the same estopo version on both`) ; elle reprend seule une fois les versions alignées.

## Dépannage

| Message | Cause et remède |
|---|---|
| Au démarrage : `ESTOPO_STANDBY_OF requires ESTOPO_REPLICATION_TOKEN_FILE` ou `a standby needs ESTOPO_REPLICATION_TOKEN_FILE` | Le fichier de jeton n'est pas configuré sur la standby. |
| Au démarrage : `replication token …: at least 16 characters are required` | Jeton trop court. |
| Au démarrage : `replication CA …: no certificate found` ou `replication CA: open …` | Le fichier de `ESTOPO_REPLICATION_CA` est vide, illisible ou n'est pas au format PEM. |
| Au démarrage : `replication over plain HTTP on a non-loopback address is refused…` | L'instance a un jeton mais ne sert pas HTTPS. |
| Au démarrage : `primary URL: …: the scheme must be https` (ou `the replication token would travel in clear`) | `ESTOPO_STANDBY_OF` doit être une URL `https://`. |
| « Dernière erreur » : `primary unreachable: …` | Réseau, nom, port, certificat. `x509: certificate signed by unknown authority` : renseignez `ESTOPO_REPLICATION_CA`. Si la machine a des variables `HTTPS_PROXY`, le client de réplication les suit : ajoutez l'autre instance à `NO_PROXY`. |
| « Dernière erreur » : `… invalid replication token` | Les deux fichiers de jeton diffèrent. |
| « Dernière erreur » : `… is not a primary (role standby)` | L'instance suivie n'est pas primaire : les deux instances sont standby. Promouvez celle qui doit collecter. |
| « Dernière erreur » : `… 404 …` sur `/api/v1/replication/…` | Le primaire n'a pas de jeton configuré : son interface de réplication est désactivée. |
| « Dernière erreur » : `meta.db schema … run the same estopo version on both` | Versions différentes. |
| Bouton « Rétrograder en standby » : `a standby needs ESTOPO_REPLICATION_TOKEN_FILE` | Cette instance n'a pas de jeton : ajoutez-le et redémarrez. |
| Bouton « Reprendre la collecte » : `… is still the primary (promoted at …): demote it first` | L'autre instance est toujours primaire. |
