*(English version: [clusters.md](clusters.md))*

# Déclarer et configurer un cluster Elasticsearch

Ce guide explique, de bout en bout, comment faire suivre un cluster par ES.Topo : ce qu'il faut créer côté Elasticsearch, où placer les secrets, comment remplir le formulaire, puis comment régler la collecte et l'affichage. Il suppose qu'ES.Topo est installé (voir [Installation](installation_fr.md)) et que vous êtes administrateur de l'application.

## Vue d'ensemble

1. Côté Elasticsearch, créez un rôle en lecture seule, puis une identité qui porte ce rôle : une clé d'API, un utilisateur avec mot de passe, ou un certificat client.
2. Sur la machine d'ES.Topo, placez le secret correspondant dans un **fichier**.
3. Dans **Administration › Clusters**, remplissez le formulaire, cliquez sur « Tester la connexion », puis sur « Enregistrer ».
4. Vérifiez dans **Administration › Collecteurs** que la première sonde et la première capture ont réussi.

ES.Topo ne fait que lire. Son client refuse d'émettre autre chose qu'une requête `GET` ou `HEAD`.

## Côté Elasticsearch : le compte en lecture seule

Les requêtes ci-dessous se lancent dans la console de Kibana (Dev Tools) ou avec `curl`, avec un compte autorisé à gérer la sécurité.

### Le rôle

```
PUT _security/role/estopo_reader
{
  "cluster": ["monitor"],
  "indices": [
    {
      "names": ["*"],
      "privileges": ["monitor", "view_index_metadata"],
      "allow_restricted_indices": true
    }
  ]
}
```

Ce sont exactement les privilèges que vérifie « Tester la connexion » : le privilège de cluster `monitor`, et les privilèges d'index `monitor` et `view_index_metadata` sur tous les indices, indices système compris (`allow_restricted_indices`). Aucun privilège de lecture des documents n'est demandé : ES.Topo ne lit ni les documents ni les mappings.

Choisissez ensuite **une** des trois identités suivantes.

### Option 1 : une clé d'API

```
POST _security/api_key
{
  "name": "estopo",
  "role_descriptors": {
    "estopo_reader": {
      "cluster": ["monitor"],
      "indices": [
        {
          "names": ["*"],
          "privileges": ["monitor", "view_index_metadata"],
          "allow_restricted_indices": true
        }
      ]
    }
  }
}
```

La réponse contient `id`, `api_key` et, sur les versions récentes, `encoded`. Écrivez dans le fichier de secret **soit** la valeur de `encoded`, **soit** le texte `id:api_key` (les deux valeurs séparées par deux-points) : ES.Topo accepte les deux formes. Une clé d'API ne peut pas avoir plus de privilèges que le compte qui la crée.

### Option 2 : un utilisateur avec mot de passe

```
PUT _security/user/estopo
{
  "password": "<mot de passe long et aléatoire>",
  "roles": ["estopo_reader"]
}
```

Écrivez le mot de passe, seul, dans le fichier de secret.

### Option 3 : un certificat client

ES.Topo présente un certificat client pendant la négociation TLS. Il faut donc que le cluster accepte les certificats clients sur son interface HTTP et qu'un realm PKI y soit configuré : c'est une configuration d'Elasticsearch, décrite dans sa documentation. Le rôle est ensuite attribué par une correspondance de rôle :

```
PUT _security/role_mapping/estopo_pki
{
  "roles": ["estopo_reader"],
  "enabled": true,
  "rules": {
    "all": [
      { "field": { "realm.name": "pki1" } },
      { "field": { "username": "estopo" } }
    ]
  }
}
```

Remplacez `pki1` par le nom de votre realm PKI et `estopo` par le nom d'utilisateur que ce realm tire du certificat (par défaut, son CN). Cette authentification a été testée sur les versions 7.17.29, 8.19.22 et 9.5.4 avec une licence d'essai ; le niveau de licence qu'exige le realm PKI en production n'a pas été vérifié.

## Les fichiers de secrets

ES.Topo ne stocke jamais un mot de passe, une clé d'API ou une clé privée : sa configuration ne contient que le **chemin** d'un fichier, que le service lit lui-même.

| Fichier | Contenu attendu |
|---|---|
| Clé d'API | une ligne : la valeur `encoded`, ou `id:api_key` |
| Mot de passe | une ligne : le mot de passe (les espaces et retours à la ligne en début et en fin sont ignorés) |
| Certificat client | PEM ; peut contenir la chaîne complète |
| Clé privée | PEM. Si elle est chiffrée, seul le format PKCS#8 avec PBES2 et AES (celui que produit OpenSSL par défaut) est accepté |
| Phrase de passe de la clé | une ligne : la phrase de passe |
| CA | un fichier PEM, ou un dossier dont tous les fichiers `.pem`, `.crt` et `.cer` sont chargés (sans descendre dans les sous-dossiers) |

Où les mettre, avec l'installation décrite dans [Installation](installation_fr.md) :

```bash
install -o root -g estopo -m 0640 prod-apikey /etc/estopo/secrets/prod-apikey
```

- Le dossier `/etc/estopo/secrets` appartient à `root`, groupe `estopo`, mode `0750` ; les fichiers sont en mode `0640`. Le service peut les lire, pas les modifier.
- **`ESTOPO_SECRETS_DIR`** : quand cette variable est définie (le fichier d'environnement d'exemple la fixe à `/etc/estopo/secrets`), tout chemin saisi dans l'administration doit désigner un fichier existant situé dans ce dossier, liens symboliques résolus. Sinon l'enregistrement ou le test est refusé avec le message `… not an existing file of the secrets directory (ESTOPO_SECRETS_DIR)`. Sans cette variable, un administrateur de l'application peut faire lire au service n'importe quel fichier que son compte système peut lire : définissez-la.
- **Rotation** : les fichiers sont lus quand la collecte d'un cluster démarre. Après avoir changé le contenu d'un fichier, **redémarrez le service** (`systemctl restart estopo`) ; le nouveau contenu n'est pas relu autrement.
- En haute disponibilité, les mêmes fichiers doivent exister aux mêmes chemins sur les deux machines.

## La CA par défaut

En haut de **Administration › Clusters**, le champ « CA par défaut des clusters (fichier ou dossier ; vide = CA système) » indique la ou les autorités de certification qui ont signé les certificats de vos clusters. Elle sert à tous les clusters qui n'ont pas leur propre CA.

1. Copiez le certificat de votre CA racine et ceux des CA intermédiaires, au format PEM, dans un dossier, par exemple `/etc/estopo/secrets/ca/`.
2. Saisissez ce chemin et cliquez sur « Enregistrer ».
3. Résultat attendu : « Enregistré : N certificat(s) chargé(s). ». Une erreur signifie que le chemin n'existe pas, n'est pas lisible ou ne contient aucun certificat.

Si le champ est vide et qu'un cluster n'a pas de CA propre, ce sont les autorités connues du système d'exploitation qui sont utilisées.

ES.Topo vérifie toujours le certificat du cluster et le nom d'hôte de l'URL. **Il n'existe pas d'option pour désactiver cette vérification** : l'URL doit employer un nom présent dans le certificat.

## Le formulaire, champ par champ

Dans **Administration › Clusters**, cliquez sur « + Nouveau cluster ».

| Champ | Signification | Valeurs et défaut |
|---|---|---|
| « Identifiant (minuscules, chiffres, - et _) » | Nom technique du cluster dans ES.Topo : il nomme ses dossiers et figure dans les adresses des pages. | 63 caractères au plus, commence par une lettre minuscule ou un chiffre. **Ne peut plus être changé** après la création. |
| « Nom affiché » | Nom montré dans l'interface. | Par défaut : l'identifiant. |
| « URL(s), une par ligne » | Adresses HTTP(S) du cluster. | Une seule suffit (une adresse virtuelle, par exemple). S'il y en a plusieurs, elles sont essayées dans l'ordre quand la connexion échoue ; celle qui a répondu en dernier est essayée en premier. |
| « Authentification ES » | Façon de s'identifier. | « Certificat client (mTLS) » (défaut), « Clé d'API », « Utilisateur / mot de passe », « Aucune ». |
| « Timeout des appels (s) » | Durée maximale de chaque appel. | 60 secondes. Le délai de connexion est de 10 secondes et ne se règle pas. |
| « Fichier du certificat client », « Fichier de la clé privée », « Fichier de la passphrase (optionnel) » | Pour « Certificat client (mTLS) ». | Chemins de fichiers. |
| « Fichier de la clé d'API » | Pour « Clé d'API ». | Chemin de fichier. |
| « Utilisateur », « Fichier du mot de passe » | Pour « Utilisateur / mot de passe ». | Nom du compte ; chemin de fichier. |
| « CA : fichier ou dossier (vide = CA par défaut, sinon CA système) » | CA propre à ce cluster. | Vide dans le cas général. |
| « Ajouter le magasin de CA du système » | Accepte aussi les autorités connues du système, en plus de la CA indiquée. | Décoché. |
| « Attribut de nœud désignant la machine (vide = host) » | Voir [Hiérarchie et attribut machine](#hiérarchie-de-navigation-et-attribut-machine). | Vide. |
| « Collecte active » | Décochez pour arrêter la collecte sans rien supprimer. | Coché. |
| « Référence de nœuds déclarée (optionnelle, un nom par ligne) » | Voir [Référence de nœuds et acquittements](#référence-de-nœuds-et-acquittements). | Vide. |
| « Hiérarchie de navigation (…) » | Voir [Hiérarchie et attribut machine](#hiérarchie-de-navigation-et-attribut-machine). | Vide = automatique. |
| Section « Planification » | Voir [Planification](#planification). | Vide = valeurs par défaut. |

Règles de sécurité appliquées à l'enregistrement et au démarrage de la collecte :

- Les URL doivent commencer par `https://` ou `http://`.
- Avec « Clé d'API » ou « Utilisateur / mot de passe », une URL `http://` est **refusée**, sauf vers la machine locale (`localhost` ou `127.0.0.1`) : le secret circulerait en clair.
- Les redirections HTTP ne sont jamais suivies, et les variables de proxy du système sont ignorées : ES.Topo joint les clusters directement.

Les règles de familles et les acquittements apparaissent sous le formulaire une fois le cluster enregistré.

## Tester la connexion

Le bouton « Tester la connexion » essaie la configuration **telle qu'elle est saisie**, même non enregistrée, sans rien modifier. Il effectue trois appels : `GET /`, `GET /_cluster/health` et `GET /_security/user/_has_privileges`.

Comment lire le résultat :

| Affichage | Signification |
|---|---|
| « Connexion OK : *nom* *version*, *N* nœuds, santé *couleur*. » | Le cluster répond et l'identité est acceptée. |
| « Privilèges manquants : cluster:monitor, index:\*:view_index_metadata… » (encadré orange) | L'identité est acceptée mais le rôle est incomplet. Chaque élément nomme un privilège de cluster (`cluster:…`) ou d'index (`index:*:…`) absent. Corrigez le rôle. |
| « Échec : … » | L'appel a échoué. Le texte qui suit est l'erreur ; voir [Erreurs fréquentes](#erreurs-fréquentes). |

Limite : sur un cluster dont la sécurité est désactivée, l'appel `_has_privileges` n'existe pas ; le comportement du test dans ce cas n'a pas été vérifié sur un vrai cluster.

Sur une instance standby, le test est refusé : une standby ne contacte jamais les clusters.

Après « Enregistrer », la collecte démarre aussitôt : une sonde, puis une première capture complète. Le cluster apparaît sur la page « Clusters » ; tant que rien n'est arrivé, il affiche « Pas encore de capture ».

## Planification

ES.Topo fait deux sortes de relevés :

- la **sonde légère**, fréquente, lit la santé, la liste des nœuds et leurs métriques ;
- la **capture complète** lit en plus tous les indices et tous les shards. Elle est déclenchée quand la sonde voit un changement, et au moins de temps en temps.

Section « Planification » du formulaire (laissez vide pour garder la valeur par défaut) :

| Champ | Défaut | Rôle | Limites |
|---|---|---|---|
| « Sonde légère toutes les (min) » | 5 | Période de la sonde. C'est la précision avec laquelle on date le départ d'un nœud. | 0,5 minute au moins |
| « Capture complète au plus toutes les (min) » | 10 | Délai minimal entre deux captures complètes, pour ne pas charger un cluster en difficulté. Un déclenchement survenu pendant ce délai est reporté, pas perdu. | au moins la période de sonde |
| « Capture complète au moins toutes les (min) » | 60 | Une capture complète est faite même si rien ne change. | au moins le délai minimal |
| « Jitter (% de la période) » | 10 | Part de hasard ajoutée à la période de sonde, pour que les clusters ne soient pas tous interrogés au même instant. | 0 à 50 |
| « Seuil de déclenchement (shards en mouvement) » | 0 | Une capture complète est déclenchée quand le nombre de shards en relocation, en initialisation ou non assignés a changé de plus que ce seuil depuis la dernière capture complète. | 0 ou plus |

Ce qui déclenche une capture complète : la première sonde, un changement de santé, un changement de l'ensemble des nœuds, des shards en mouvement au-delà du seuil, un changement des cluster settings, le bouton « Capturer maintenant », et le délai maximal.

Le bouton « Capturer maintenant » respecte le délai minimal ; cochez « Forcer (ignorer le délai minimum) » pour passer outre.

Le nombre de captures complètes simultanées, tous clusters confondus, se règle dans **Administration › Collecteurs** : « Captures complètes simultanées (tous clusters) », de 1 à 32, 3 par défaut. Ce réglage est pris en compte au prochain redémarrage.

## Hiérarchie de navigation et attribut machine

La vue physique range les nœuds par niveaux. Par défaut : les attributs d'awareness du cluster (`cluster.routing.allocation.awareness.attributes`, dans l'ordre), puis le tier, puis la machine, puis le nœud.

**Champ « Hiérarchie de navigation »** : une liste de mots séparés par des virgules.

- `tier`, `machine` et `node` sont des niveaux intégrés.
- Tout autre mot est le **nom d'un attribut de nœud** (`node.attr.<nom>` dans la configuration d'Elasticsearch).
- `node`, s'il est présent, doit être le dernier. Un niveau ne peut pas figurer deux fois.

Exemples :

| Saisie | Effet |
|---|---|
| *(vide)* | attributs d'awareness, tier, machine, node |
| `dc, tier, machine, node` | datacenter, puis tier, puis machine |
| `dc, rack, tier, machine, node` | un niveau « rack » entre le datacenter et le tier |
| `tier, machine, node` | pas de niveau zone |

La hiérarchie est appliquée à l'affichage : la changer réorganise aussi les dates passées.

**Champ « Attribut de nœud désignant la machine (vide = host) »** : quand plusieurs nœuds Elasticsearch tournent sur une même machine physique, ES.Topo les regroupe. Par défaut, la machine est le champ `host` du nœud, et son adresse IP si `host` est vide. Si vos nœuds portent un attribut qui nomme la machine (par exemple `node.attr.machine`), saisissez son nom ici. La machine est enregistrée à chaque capture complète : un changement de ce champ vaut pour les captures suivantes.

Le **tier** d'un nœud vient de ses rôles (`data_hot`, `data_warm`, `data_cold`, `data_frozen`, `data_content`, `data`). Un nœud sans rôle de données est rangé sous « master » ou « coordinating ».

## Familles d'indices

Une famille regroupe les indices d'une même série : c'est l'unité de la vue logique et de plusieurs écrans.

### Détection automatique

La famille est déduite **du seul nom de l'index**, en retirant à la fin, autant de fois que possible :

- un compteur de rollover de six chiffres : `-000001` ;
- une date : `2026.09.28`, `2026-09-28`, `20260928`, `2026.09`, `2026.w40` ;
- une année seule, entre **1990 et 2099**, précédée d'un séparateur : `-2026`.

Exemples :

| Index | Famille |
|---|---|
| `logs-app-2026.09.28` | `logs-app` |
| `weblog-000002` | `weblog` |
| `.ds-logs-app-2026.09.28-000001` | `.ds-logs-app` |
| `metrics-8080` | `metrics-8080` (8080 n'est pas une année entre 1990 et 2099) |
| `archive-2031` | `archive` |
| `clients` | `clients` (un nom sans suffixe est sa propre famille) |

Ni les alias, ni les data streams, ni les politiques ILM ne sont consultés.

### Règles par cluster

Quand la détection ne convient pas, ouvrez le cluster dans **Administration › Clusters** : la carte « Règles de familles » accepte une expression régulière par ligne (syntaxe RE2, celle de Go).

- La première règle qui correspond au nom de l'index l'emporte.
- La famille est le groupe nommé `(?P<family>…)` s'il existe, sinon le premier groupe, sinon tout le texte qui correspond.
- Si aucune règle ne correspond, la détection automatique s'applique.
- Une expression n'est pas ancrée : ajoutez `^` et `$` si nécessaire.

Exemples :

| Règle | Effet |
|---|---|
| `^(?P<family>logs-[a-z]+)-` | `logs-app-eu-2026.09.28` et `logs-app-us-2026.09.28` vont dans la famille `logs-app` |
| `^shop-(\d+)-orders` | `shop-0042-orders-2026.09` va dans la famille `0042` (premier groupe) |
| `^tmp-` | tous les indices `tmp-…` vont dans la famille `tmp-` (texte qui correspond) |

Avant d'enregistrer, cliquez sur « Aperçu du regroupement » : le tableau liste les familles obtenues avec les règles saisies, le nombre d'indices de chacune et jusqu'à trois exemples. L'aperçu porte sur les indices actuels du cluster : il faut qu'une capture complète ait déjà eu lieu. Cliquez ensuite sur « Enregistrer ».

Les familles sont calculées à l'affichage avec les règles en vigueur : changer une règle regroupe aussi l'historique.

## Référence de nœuds et acquittements

ES.Topo signale les **nœuds manquants** : ceux qui devraient être là et n'y sont pas.

- La référence est l'ensemble des nœuds vus au moins une fois pendant les **7 jours** qui précèdent la date consultée.
- Le champ « Référence de nœuds déclarée » ajoute des noms à cette référence. Un nœud déclaré et jamais vu apparaît donc manquant : utile pour un nœud attendu qui n'a jamais rejoint le cluster.
- Un nœud retiré définitivement cesse d'être compté manquant au bout de 7 jours. Pour ne pas attendre, **acquittez-le**.

Acquitter un nœud (carte « Nœuds acquittés (retirés durablement) », sous le formulaire) :

1. Saisissez le nom du nœud dans « Nœud », un « Commentaire » si vous le souhaitez, et la date « Retiré à partir du » (vide = maintenant).
2. Cliquez sur « Acquitter ».

Le nœud n'est plus compté manquant à partir de cette date. L'action est inscrite au journal d'audit. « Annuler » retire l'acquittement.

## Cycle de vie d'un cluster

| Action | Effet | Réversible |
|---|---|---|
| Décocher « Collecte active », puis « Enregistrer » | La collecte s'arrête. L'historique reste consultable. | Oui : recochez. |
| « Retirer de la configuration » | La collecte s'arrête et le cluster disparaît de l'administration. Sur la page « Clusters », il reste visible avec la mention « Retiré de la configuration », tant que la rétention n'a pas supprimé son historique. | Oui : créez un cluster avec le même identifiant, il retrouve son historique. |
| « Purger l'historique » | Supprime tout de suite et définitivement tout l'historique du cluster. | **Non.** |

**L'ordre compte.** Pour supprimer complètement un cluster :

1. décochez « Collecte active » et enregistrez (sinon la prochaine capture recrée un historique) ;
2. cliquez sur « Purger l'historique » ;
3. cliquez sur « Retirer de la configuration ».

Une fois le cluster retiré, son formulaire n'est plus accessible, donc le bouton de purge non plus.

### Cluster suspendu après un changement d'identité

À la première capture, ES.Topo lie la configuration à l'identifiant unique du cluster (`cluster_uuid`), affiché en haut du formulaire : « Lié au cluster_uuid … ». Si, plus tard, l'URL répond avec un autre `cluster_uuid` (adresse virtuelle repointée, cluster reconstruit), la collecte est **suspendue** : rien n'est enregistré, pour ne pas mélanger deux clusters dans un même historique. Le formulaire affiche « Collecte suspendue : cluster_uuid changed from … to … », et la page « Clusters » affiche « Collecte suspendue ».

Deux réponses possibles, dans le formulaire du cluster :

- **C'est bien le même cluster logique, à suivre dans le même historique** : cliquez sur « Tester la connexion » (le test lit le nouvel identifiant), puis sur « Accepter la nouvelle identité ».
- **C'est un autre cluster** : cliquez sur « Rattacher comme nouveau cluster » et saisissez un nouvel identifiant. Une nouvelle configuration reprend les mêmes paramètres de connexion ; l'ancienne est désactivée et garde son historique.

## Liste blanche des settings d'index

ES.Topo garde l'historique de certains settings d'index seulement, ceux qui expliquent l'allocation. La liste se règle pour tous les clusters dans **Administration › Settings d'index**, un nom par ligne, le joker `*` étant accepté. Liste par défaut :

```
index.number_of_shards
index.number_of_replicas
index.auto_expand_replicas
index.routing.allocation.*
index.routing.rebalance.enable
index.blocks.*
index.unassigned.node_left.delayed_timeout
index.priority
index.refresh_interval
index.translog.durability
index.lifecycle.name
index.version.created
index.creation_date
index.uuid
index.hidden
```

Un nom ne peut contenir que des lettres, des chiffres, `.`, `_`, `-` et `*`. Les settings absents de la liste ne sont ni lus ni stockés. L'écran « Contraintes » s'appuie sur `index.routing.allocation.*`, `index.number_of_replicas` et `index.auto_expand_replicas` : ne les retirez pas. Les cluster settings, eux, sont tous conservés.

## Diagnostiquer : la page Collecteurs

**Administration › Collecteurs** affiche une ligne par cluster en cours de collecte :

| Colonne | Contenu |
|---|---|
| « Cluster » | Nom et identifiant ; le motif de suspension s'il y a lieu. |
| « Dernière sonde » | Date, durée et statut de la dernière sonde légère. |
| « Dernière capture complète » | Date, durée, statut et déclencheur (`first`, `health_changed`, `nodes_changed`, `shards_changed`, `cluster_settings_changed`, `manual`, `max_interval`). |
| « Prochaine sonde » | Date prévue. |
| « Capture en attente » | Déclencheur d'une capture reportée par le délai minimal. |
| « Erreurs récentes » | Les deux dernières erreurs. |

Le bouton ▼ détaille chaque appel à Elasticsearch du dernier relevé : nom, statut HTTP, durée et taille de la réponse. C'est là que l'on mesure le coût réel de la collecte sur un cluster.

Statuts d'un relevé :

- `ok` : tout a été lu ;
- `partial` : un appel a échoué. Les métriques obtenues sont gardées, mais rien n'est conclu sur la structure : aucun nœud, index ou shard n'est déclaré disparu ;
- `failed` : le cluster n'a pas répondu à `GET /` ou à `_cluster/health`. La période apparaît comme « inconnue » dans l'interface.

**Un cluster enregistré mais absent de cette page** n'a pas pu démarrer sa collecte, le plus souvent parce qu'un fichier de secret ou de CA est illisible. L'enregistrement réussit quand même ; l'erreur est dans le journal du service (`journalctl -u estopo | grep "apply configuration"`). « Tester la connexion » donne la même erreur directement.

## Ce qui est appelé sur Elasticsearch

Chaque requête porte les en-têtes `X-Opaque-Id: estopo` et `User-Agent: estopo/<version>`, ce qui permet de la reconnaître dans les journaux et la liste des tâches du cluster.

**Sonde légère** (toutes les 5 minutes par défaut) :

| Appel | Usage |
|---|---|
| `GET /` | nom, version et `cluster_uuid` |
| `GET /_cluster/health` | santé et compteurs de shards |
| `GET /_nodes/stats/os,process,jvm,fs` (filtré) | présence des nœuds, CPU, heap, disque |
| `GET /_cat/allocation?format=json&bytes=b` | shards et disque par nœud |
| `GET /_cluster/settings?flat_settings=true` | cluster settings |
| `GET /_remote/info` | clusters distants (CCS) |

**Capture complète** : les appels de la sonde (les cluster settings étant alors lus avec `include_defaults=true`), plus :

| Appel | Usage |
|---|---|
| `GET /_nodes/os,jvm,http,transport` (filtré) | rôles, attributs, versions, adresses |
| `GET /_cat/indices?format=json&bytes=b&expand_wildcards=all&h=…` | indices |
| `GET /_all/_settings/<liste blanche>?flat_settings=true&expand_wildcards=all` | settings d'index |
| `GET /_cat/shards?format=json&bytes=b&h=…` | copies de shards |

Charge :

- la sonde consiste en six petites requêtes ;
- la capture complète pèse surtout par `_cat/shards` et `_all/_settings` : sur un cluster de 250 000 copies de shards, la réponse de `_cat/shards` est de l'ordre de 60 Mo ;
- il y a au plus une capture complète par cluster par délai minimal (10 minutes par défaut), et au plus trois captures complètes en même temps, tous clusters confondus ;
- le coût réel sur un grand cluster de production n'a pas été mesuré : relevez-le sur la page Collecteurs et allongez les délais s'il le faut.

Si le nœud qui répond à `_remote/info` n'a pas le rôle `remote_cluster_client`, Elasticsearch renvoie une erreur 400 : ES.Topo la traite comme « information CCS indisponible », pas comme un échec. Si `_nodes` signale que des nœuds n'ont pas répondu, la capture est `partial`.

## Erreurs fréquentes

| Message (dans « Échec : … », la page Collecteurs ou le journal) | Cause et remède |
|---|---|
| `x509: certificate signed by unknown authority` | La CA du cluster n'est pas connue. Renseignez la CA par défaut ou celle du cluster. |
| `x509: certificate is valid for …, not …` | Le nom de l'URL n'est pas dans le certificat du cluster. Employez un nom que le certificat contient. |
| `HTTP 401: security_exception: …` | Identité refusée : clé d'API révoquée, mot de passe erroné, certificat non reconnu par le realm. |
| `HTTP 403: security_exception: …` | Identité acceptée mais privilège manquant. Vérifiez le rôle avec « Tester la connexion ». |
| `API key file: open …: permission denied` (ou `password file`, `client key`…) | Le fichier n'est pas lisible par l'utilisateur du service. Vérifiez le propriétaire et les droits. |
| `… not an existing file of the secrets directory (ESTOPO_SECRETS_DIR)` | Le chemin est hors du dossier des secrets, ou le fichier n'existe pas. |
| `URL "…": basic authentication over plain http is refused …` | Mot de passe ou clé d'API sur une URL `http://`. Employez `https://`. |
| `client key … is encrypted but no passphrase file is configured` | Renseignez « Fichier de la passphrase ». |
| `unsupported key encryption … (only PBES2 is supported; re-encrypt with openssl pkcs8 -topk8 -v2 aes256)` | Clé chiffrée dans un ancien format. Convertissez-la avec la commande indiquée. |
| `no CA certificate found in …` | Le fichier ou le dossier de CA ne contient aucun certificat PEM. |
| `context deadline exceeded` ou `Client.Timeout` | Le cluster n'a pas répondu dans le délai. Vérifiez le réseau ; augmentez « Timeout des appels (s) » pour un très grand cluster. |
| `nodes: N of M nodes did not answer` | Des nœuds n'ont pas répondu à `_nodes` : capture `partial`, réessayée au relevé suivant. |
| `at least one URL is required`, `invalid cluster id …` | Formulaire incomplet ou identifiant invalide. |
| `this instance is standby: changes are made on the primary` | Vous êtes sur l'instance standby. Faites la modification sur le primaire. |
