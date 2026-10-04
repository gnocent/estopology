*(English version: [SECURITY.md](SECURITY.md))*

# Sécurité

Ce document décrit, en termes simples, ce qu'ES.Topo protège et comment, ce qu'il faut régler avant de l'exposer, ses limites connues, et la façon de signaler une vulnérabilité. ES.Topo est en version bêta (0.8.0-beta) et n'a fait l'objet d'aucun audit de sécurité externe.

## Modèle de sécurité

### Envers Elasticsearch

- ES.Topo ne fait que **lire** les clusters. Son client HTTP refuse, à deux niveaux, toute requête autre que `GET` ou `HEAD`.
- Le compte qu'il utilise n'a besoin que des privilèges `monitor` (cluster) et `monitor` et `view_index_metadata` (indices) : il ne peut lire ni les documents ni les mappings. Voir [Déclarer un cluster](docs/clusters_fr.md).
- Le certificat du cluster et son nom d'hôte sont toujours vérifiés, en TLS 1.2 au minimum. Il n'existe pas d'option pour désactiver cette vérification.
- Un mot de passe ou une clé d'API n'est jamais envoyé sur une URL `http://`, sauf vers la machine locale.
- Les redirections ne sont pas suivies, et la taille des réponses est bornée.

### Ce que contient l'instance

Le dossier de données contient la topologie de vos clusters au fil du temps : noms et adresses des nœuds, noms des indices, settings, événements. Il contient aussi la configuration (URL des clusters, **chemins** des fichiers de secrets), les comptes locaux (empreintes argon2id des mots de passe), les sessions (empreintes des jetons) et le journal d'audit. Il ne contient ni documents indexés, ni mots de passe en clair, ni clés.

### Qui peut quoi

| Qui | Ce qu'il peut faire |
|---|---|
| **Un visiteur non connecté**, authentification activée | Voir l'écran de connexion, appeler `/healthz` (version, rôle, nombre de clusters) et, s'il est activé, `/metrics`. Rien d'autre. |
| **Un visiteur**, authentification désactivée (mode par défaut) | **Tout** : il est administrateur. |
| **Un lecteur** | Consulter l'historique de tous les clusters et l'exporter ; changer son propre mot de passe. Il ne voit pas l'administration (configuration, chemins de fichiers, comptes, audit). |
| **Un administrateur** | Tout régler : clusters, rétention, comptes, authentification, haute disponibilité. Il peut faire contacter par le serveur n'importe quelle URL, et lui faire lire les fichiers dont il saisit le chemin (voir [Limites connues](#limites-connues)). |
| **Le détenteur du jeton de réplication** | Lire, par l'interface de réplication, toute la configuration et tout l'historique, **y compris les empreintes des mots de passe** et le journal d'audit. Il ne peut rien modifier. |
| **Un compte système qui lit le dossier de données** | Lire tout ce que contient l'instance. |

### Réglages par défaut

- **Écoute locale.** Sans réglage, ES.Topo n'écoute que sur `127.0.0.1:8080`. L'exposer demande de définir `ESTOPO_LISTEN`.
- **Authentification sur transport sûr seulement.** Les modes « Comptes locaux » et « LDAP » sont refusés en HTTP clair sur une écoute non locale, au démarrage comme au changement de mode.
- **Refus en cas de doute.** Si les réglages d'authentification ou les sessions ne peuvent pas être lus, les requêtes sont refusées (réponse 503) et le démarrage échoue : ES.Topo ne se comporte jamais comme si l'authentification était désactivée.
- **Protection contre les requêtes d'un autre site.** Toute requête de modification doit venir de la même origine que l'interface, porter un corps JSON ou un en-tête propre à l'interface, et, quand l'authentification est activée, un jeton anti-CSRF lié à la session. Sinon elle est refusée (réponse 403).
- **Sessions.** Cookie inaccessible aux scripts, limité au même site, `Secure` en HTTPS ; expiration après inactivité (12 heures par défaut) et durée maximale de 7 jours ; fermeture des autres sessions au changement de mot de passe, de rôle, ou à la désactivation du compte.
- **Mots de passe.** 12 caractères au moins, empreinte argon2id. Les tentatives de connexion sont limitées par utilisateur et par adresse.
- **Secrets en fichiers.** Les mots de passe, clés d'API, clés privées et le jeton de réplication ne sont jamais saisis dans l'interface ni stockés dans la base : seul leur chemin l'est.
- **En-têtes du navigateur.** Politique de sécurité de contenu stricte (aucune ressource externe, pas d'inclusion dans une autre page), `X-Content-Type-Options: nosniff`, `Referrer-Policy: no-referrer`, et `Strict-Transport-Security` quand ES.Topo sert HTTPS.
- **Aucun appel sortant** autre que vers vos clusters, votre annuaire et l'autre instance de la paire : pas de télémétrie, pas de vérification de mise à jour, aucune ressource chargée depuis Internet par l'interface.
- **Journal d'audit** des connexions et des actions d'administration, gardé un an.
- **Exports** : dans les fichiers CSV, les cellules qui commencent comme une formule sont neutralisées.

## Liste de durcissement

Avant d'ouvrir le service à d'autres que vous :

1. **Activez l'authentification avant d'exposer le service** : soit avec `ESTOPO_ADMIN_PASSWORD` au premier démarrage, soit en créant un administrateur sur une écoute locale. Voir [Installation](docs/installation_fr.md#sécuriser-le-premier-démarrage).
2. **Servez HTTPS** (`ESTOPO_TLS_CERT`, `ESTOPO_TLS_KEY`), directement ou par un proxy inverse sur la même machine.
3. **Définissez `ESTOPO_SECRETS_DIR`** sur un dossier qui ne contient que les secrets d'ES.Topo.
4. Faites tourner le service sous un **compte système dédié**, avec l'unité systemd fournie, qui limite ce que le service peut écrire et voir.
5. Donnez aux fichiers de secrets et au fichier d'environnement les droits `0640`, propriétaire `root`, groupe du service ; au dossier de données, `0750`, propriétaire du service.
6. Côté Elasticsearch, utilisez un compte ou une clé d'API **dédié**, limité au rôle en lecture seule.
7. N'employez pas `ESTOPO_ALLOW_INSECURE_AUTH` hors d'un réseau de test.
8. Supprimez `ESTOPO_ADMIN_PASSWORD` du fichier d'environnement après le premier démarrage, et changez ce mot de passe.
9. En mode LDAP, gardez un compte local de secours avec un mot de passe long, conservé en lieu sûr.
10. Si vous activez `/metrics`, filtrez l'accès à ce chemin si les identifiants de vos clusters ne doivent pas être publics sur votre réseau.
11. En haute disponibilité, protégez le fichier de jeton comme une clé privée, et activez l'authentification sur les deux instances.
12. Limitez par un pare-feu les machines qui peuvent joindre le port du service.
13. Vérifiez la somme SHA-256 des binaires téléchargés, et tenez ES.Topo à jour.

## Limites connues

- **Sans authentification, tout ce qui atteint le service est administrateur.** Sur l'écoute locale par défaut, cela inclut tous les utilisateurs et tous les programmes de la machine. Cela inclut aussi une page web malveillante ouverte dans un navigateur de cette machine, par la technique dite de *DNS rebinding* : ES.Topo ne vérifie pas le nom d'hôte sous lequel il est appelé. Activez l'authentification avant d'exposer le service, et sur toute machine partagée.
- **Un administrateur de l'application peut faire lire au serveur des fichiers de la machine.** Les champs de chemin (clé d'API, mot de passe, certificat, CA) sont lus par le service ; en les combinant avec une URL de son choix, un administrateur peut envoyer leur contenu vers un serveur qu'il contrôle. Sans `ESTOPO_SECRETS_DIR`, cela vaut pour tout fichier lisible par le compte du service ; avec, seulement pour les fichiers de ce dossier. La vérification a lieu à l'enregistrement et au test : définissez la variable dès l'installation.
- **Un administrateur peut faire émettre des requêtes de lecture par le serveur vers n'importe quelle adresse** que celui-ci peut joindre (URL de cluster, d'annuaire, d'instance surveillée).
- **`/healthz` est toujours public** ; `/metrics`, quand il est activé, l'est aussi.
- **Le jeton de réplication donne un accès complet en lecture**, empreintes des mots de passe comprises. Si le primaire est sans authentification, la standby l'est aussi, et tout visiteur peut la promouvoir.
- **Derrière un proxy inverse**, ES.Topo ne lit pas `X-Forwarded-For` : la limitation des tentatives par adresse est partagée par tous les utilisateurs, et le journal d'audit enregistre l'adresse du proxy.
- **Les binaires ne sont pas signés.** Seule la somme SHA-256 publiée permet de vérifier un téléchargement.
- **LDAP** : seul OpenLDAP est testé.
- **Le dossier de données n'est pas chiffré.** Protégez-le par les droits du système de fichiers et, si nécessaire, par un chiffrement du volume.
- **Aucun audit de sécurité externe** n'a été réalisé.

## Signaler une vulnérabilité

Ne créez pas de ticket public pour une vulnérabilité.

Utilisez le **signalement privé de vulnérabilité** de GitHub : dans le dépôt, onglet « Security », puis « Report a vulnerability ». Le signalement reste privé entre vous et les mainteneurs jusqu'à la publication d'un avis de sécurité.

Indiquez la version (`estopo version`), la configuration utile (mode d'authentification, HTTPS ou non, proxy inverse, haute disponibilité), les étapes pour reproduire et l'effet constaté. Ne joignez ni secrets ni données réelles de vos clusters.

Les corrections sont publiées dans une nouvelle version, accompagnée d'un avis de sécurité du dépôt. Pendant la phase bêta, seule la dernière version publiée reçoit des corrections.
