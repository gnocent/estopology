*(English version: [CHANGELOG.md](CHANGELOG.md))*

# Journal des versions

Ce fichier décrit ce que contient chaque version publiée d'ES.Topo. Les binaires de chaque version sont sur la [page des versions](../../releases).

## 0.8.1-beta

Corrections issues d'une revue de tout le code, chacune couverte par un test.

### Mise à jour depuis 0.8.0-beta

- **Mode LDAP** : le nom du compte est désormais lu dans l'entrée trouvée par la recherche, attribut `uid` par défaut. Si vos entrées n'ont pas d'`uid` (Active Directory), renseignez le nouveau champ « Attribut du nom de compte » (par exemple `sAMAccountName`) avant la mise à jour, sinon les connexions par l'annuaire seront refusées ; les comptes locaux restent utilisables.
- Les données et `meta.db` ne changent pas de format. Primaire et standby doivent être mis à jour ensemble.

### Sécurité

- Mode LDAP : une connexion avec un nom vide n'ouvre plus de session (elle était acceptée sans mot de passe) ; une session dont le compte n'a pas de rôle connu est refusée.
- Mode LDAP : le compte est nommé par l'entrée trouvée, pas par le nom saisi. Avec un filtre sur `uid` ou `mail`, l'adresse d'une personne désactivée ouvrait un second compte actif.
- Mode LDAP : une connexion sans compte local coûte le même calcul de mot de passe qu'un compte local ; le temps de réponse ne révèle plus quels comptes locaux existent.
- Limitation des tentatives : une connexion réussie ne remet plus à zéro le compteur de l'adresse ; le titulaire d'un compte ne peut plus essayer sans limite les mots de passe des autres. Les noms que la recherche des comptes confond (`ſam` et `sam`) partagent leur compteur.
- Deux administrateurs qui se suppriment en même temps ne laissent plus l'instance sans administrateur local.
- Changer de mode d'authentification ferme les sessions ouvertes ; l'administrateur local qui fait le changement garde la sienne. Auparavant, local → aucune → local les faisait revivre.
- Standby : un changement de mot de passe, de rôle, une désactivation ou une suppression sur le primaire ferment aussi les sessions de la standby ; un compte LDAP supprimé sur le primaire disparaît de la standby.
- Un paramètre `points` invalide de l'historique d'un nœud est refusé au lieu de renvoyer tout l'historique.
- Go 1.27.3 et excelize corrigé : `govulncheck` ne signale plus aucune vulnérabilité.

### Collecte

- Modifier la configuration d'un cluster pouvait figer l'instance quand l'ingestion était en retard ; un cluster suspendu après un changement d'identité ne pouvait plus être reconfiguré ni désactivé.
- L'association d'un cluster ne relance plus sa collecte avec une capture complète de trop.
- L'intervalle maximal force une capture complète même quand les sondes échouent ; la planification suit une horloge monotone (un recul de l'horloge ne retarde plus les sondes).
- Une capture complète dont seuls des appels annexes échouent (`_nodes/stats`, `_cat/allocation`, `_cluster/settings`, `_remote/info`) garde sa structure : la section en échec est inconnue, pas vide.

### Stockage

- Un historique en avance de plus d'une heure sur l'horloge de l'instance (horloge revenue en arrière après un saut en avant) est signalé : dans l'erreur qui refuse les nouveaux relevés, dans la métrique `estopo_history_ahead_seconds` et par un bandeau de la page Collecteurs.
- Les listes vides (dernier index supprimé, dernier remote retiré) survivent au spool : la standby et le rejeu ferment les mêmes intervalles que le primaire.
- La rétention par taille ne supprime jamais la dernière partition d'un cluster.
- Une ligne de catalogue laissée sans fichier par un crash n'empêche plus le démarrage.
- Fenêtres d'incertitude des événements de cluster settings et de remotes, settings d'index inconnus, mises à jour de nœuds à travers un changement de semaine : corrigés.

### Haute disponibilité

- Une reconstruction demandée (bouton, rétrogradation) survit à un redémarrage, et n'est plus perdue quand elle est demandée pendant une reconstruction en cours ; une standby en retard sur un spool vide rattrape ; une purge atteint la standby et ne revient pas au démarrage suivant ; l'indicateur de réplication bloquée ne clignote plus ; l'arrêt attend la réplication ; la liste des clusters de l'API de réplication n'attend plus l'ingestion.

### Exploration

- Les filtres par famille (chemin, événements, recherche) suivent les règles de familles.
- Pertes : appariement chronologique, copies d'un index supprimé ensuite comptées à part, zone lue aussi dans les réglages des nœuds.
- Les métriques de groupe ne s'effondrent plus entre deux relevés d'un nœud stable.
- Différences : un réplica promu apparaît comme promu, pas déplacé ; plus de faux changement pour un nœud sans description ou depuis une sonde sans structure.
- Chemin : la position prise en B est affichée, l'ordre est stable, une troncature est signalée. **API** : `GET /clusters/{id}/path` renvoie désormais `{"segments": [...], "truncated": true}`.
- Contraintes d'allocation : `high.max_headroom` (8.5+), `disk.threshold_enabled: false` et `require` à plusieurs valeurs sont pris en compte ; un nœud sans l'attribut ne satisfait pas `require.<attribut>: "*"`, comme dans Elasticsearch.
- Pertes : le compteur « Réplicas promus » devient « Promus, copie à refaire en B » : il ne compte que les primaires relayés dont la copie manquante n'est pas encore recréée (les autres sont dans « Réallouées »). Le lien vers l'index d'une copie perdue l'ouvre à la date de la perte.

### Interface

- La page d'accueil ne plante plus quand on perd un nœud qui portait un primaire et un réplica du même shard.
- La fenêtre A/B affichée est celle interrogée ; le bandeau temporel suit l'heure courante ; une réponse périmée n'écrase plus une réponse récente ; les erreurs d'API s'affichent au lieu d'un « Chargement » sans fin ; changer d'onglet ou de couleur ne recharge plus tout ; le champ du pair HA n'est plus effacé pendant la saisie.

## 0.8.0-beta

Première version publique. C'est une version bêta : le produit a été testé contre de vrais clusters Elasticsearch 7.17.29, 8.19.22 et 9.5.4 de test et sur des données simulées, pas encore en production à grande échelle. Le format des données et l'API peuvent encore changer avant la version 1.0.

### Collecte

- Collecte en lecture seule de plusieurs clusters Elasticsearch 7.17, 8.x et 9.x ; le client refuse toute requête autre que `GET` ou `HEAD`.
- Sonde légère périodique (santé, nœuds, métriques, allocation, cluster settings, clusters distants) et capture complète (nœuds, indices, settings d'index, copies de shards).
- Déclenchement adaptatif de la capture complète sur changement de santé, de nœuds, de shards en mouvement ou de cluster settings, avec délai minimal, délai maximal et part de hasard, réglables par cluster.
- Authentification auprès d'Elasticsearch par certificat client, clé d'API ou mot de passe ; secrets lus dans des fichiers ; CA par défaut ou par cluster ; plusieurs URL par cluster.
- Capture incomplète ou en échec enregistrée comme telle : rien n'est conclu d'une réponse partielle.
- Suspension de la collecte quand l'identité du cluster (`cluster_uuid`) change derrière une URL.

### Stockage

- Historique dans des fichiers SQLite, un par cluster et par semaine, sans dépendance externe.
- Modèle par intervalles de validité : seuls les changements sont écrits ; l'état à une date quelconque se retrouve par une requête.
- Rétention par durée et par taille maximale du dossier de données.
- Relevés bruts conservés 72 heures et rejoués après un arrêt brutal.

### Écrans d'exploration

- Accueil multi-clusters trié par gravité, avec frise de santé sur 7 jours.
- Bandeau temporel sur toutes les vues datées ; chaque vue est un permalien.
- Vue physique par zone, tier et machine ; une page par zone, tier, machine et nœud, avec zoom arrière.
- Vue logique en matrice, par famille d'indices ou par index, colonnes par nœud ou regroupées, avec filtre.
- Pages famille et index : graphiques, matrice des shards, historique des settings, chemin des shards.
- Diff entre deux dates et chemin parcouru ; pertes de copies groupées par nœud ou machine ; journal d'événements ; flux de déplacements ; contraintes d'allocation.
- Recherche globale ; exports CSV, JSON et XLSX.
- Interface en français et en anglais, thème clair ou sombre, aide dans chaque écran. Tout est embarqué : aucune ressource externe.

### Administration

- Déclaration des clusters avec test de connexion et vérification des privilèges.
- Planification, hiérarchie de navigation, attribut machine, règles de familles avec aperçu, référence de nœuds et acquittements, par cluster.
- Liste blanche des settings d'index ; état des collecteurs avec durée et taille de chaque appel ; stockage et projections ; journal d'audit.

### Authentification

- Trois modes : aucune, comptes locaux, annuaire LDAP (OpenLDAP, en LDAPS ou StartTLS) ; rôles lecteur et admin.
- Création du premier administrateur par `ESTOPO_ADMIN_PASSWORD` au premier démarrage.
- Mots de passe en argon2id, sessions à expiration, limitation des tentatives, changement de son propre mot de passe, réinitialisation en ligne de commande.
- En mode LDAP, les comptes locaux restent utilisables en secours.

### Haute disponibilité

- Instance standby passive qui recopie la configuration et les relevés du primaire ; promotion manuelle.
- Quarantaine d'un ancien primaire qui constate une promotion plus récente ; rétrogradation, reconstruction, reprise.
- Blocage de la réplication quand le primaire a été remplacé ou vidé, jusqu'à décision d'un administrateur.

### Exploitation

- Un seul binaire statique pour Linux x86-64 ; binaires Windows x64 et macOS arm64 pour l'essai et le développement.
- Réglages de démarrage par variables d'environnement ; exemple d'unité systemd.
- `/healthz` ; `/metrics` au format Prometheus, à activer ; journaux sur la sortie standard, en texte ou en JSON.

### Simulateur

- `essim` simule des clusters Elasticsearch aux formats 7.17, 8.x et 9.x, avec horloge accélérée, pannes et scénarios d'incidents.
- `essim backfill` fabrique un dossier de données de démonstration ; `essim action` provoque des incidents sur un cluster simulé.

### Durcissement avant publication

- Écoute par défaut sur `127.0.0.1`.
- Refus des requêtes quand l'authentification ne peut pas être évaluée, au lieu d'un repli sur l'absence d'authentification.
- Refus des requêtes de modification venant d'un autre site, dans tous les modes.
- `ESTOPO_SECRETS_DIR` pour confiner les fichiers désignés dans l'administration.
- Durée maximale des sessions ; fermeture des sessions au changement de mot de passe ou de rôle ; protection du dernier administrateur local.
- Refus d'envoyer un mot de passe ou une clé d'API en HTTP clair ; réponses d'Elasticsearch bornées.
- Délais sur le serveur HTTP, en-tête HSTS, politique de sécurité de contenu renforcée, neutralisation des formules dans les exports CSV.

### Limites connues

- **Version bêta.** Pas d'expérience de production à grande échelle ; le coût réel de la collecte sur un très grand cluster n'a pas été mesuré.
- **Pas de bascule automatique** en haute disponibilité ; sans instance surveillée renseignée sur le primaire, un ancien primaire qui revient reprend la collecte.
- **Sans authentification, tout visiteur est administrateur** ; c'est le mode par défaut, sur une écoute locale. Voir [SECURITY_fr.md](SECURITY_fr.md).
- **LDAP** : seul OpenLDAP est testé ; avec les filtres par défaut, seule l'appartenance directe à un groupe compte.
- **Proxy inverse** : pas de préfixe de chemin ; l'en-tête `X-Forwarded-For` n'est pas lu, donc la limitation des tentatives par adresse est partagée.
- **Secrets** : le contenu d'un fichier de secret de cluster n'est relu qu'au redémarrage. La vérification TLS des clusters ne peut pas être désactivée.
- **Sauvegarde** : pas de commande de sauvegarde à chaud ; pas de retour à une version antérieure après une mise à jour.
- **Familles d'indices** déduites du seul nom de l'index ; les data streams, le tier frozen, les searchable snapshots et le CCR ne sont pas traités spécifiquement.
- **Contraintes d'allocation** : c'est une estimation, qui ne remplace pas `_cluster/allocation/explain`.
- **Cluster retiré** : son historique ne peut plus être purgé depuis l'interface ; purgez avant de retirer.
- **Test de connexion** sur un cluster sans sécurité : comportement non vérifié.
- **`/healthz`** : le seuil « sans sonde récente » est fixé à 15 minutes.
- **Binaires non signés** ; les binaires Windows et macOS sont destinés à l'essai.
- **API HTTP** non documentée et susceptible de changer.
