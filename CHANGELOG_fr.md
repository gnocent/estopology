*(English version: [CHANGELOG.md](CHANGELOG.md))*

# Journal des versions

Ce fichier décrit ce que contient chaque version publiée d'ES.Topo. Les binaires de chaque version sont sur la [page des versions](../../releases).

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
