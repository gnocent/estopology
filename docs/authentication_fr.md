*(English version: [authentication.md](authentication.md))*

# Authentification (guide avancé)

Ce guide décrit qui peut se connecter à ES.Topo et comment : les trois modes, les comptes locaux, les sessions, l'annuaire LDAP et le cas du proxy inverse. Pour créer le tout premier administrateur, voyez d'abord [Installation › Sécuriser le premier démarrage](installation_fr.md#sécuriser-le-premier-démarrage).

Tout se règle dans **Administration › Authentification**, sauf mention contraire.

## Les trois modes

| Mode (libellé) | Qui entre | À utiliser |
|---|---|---|
| « Aucune (tout le monde est admin) » | Tout visiteur, sans se connecter, avec les droits d'administrateur. Un bandeau le rappelle : « Authentification désactivée : tout visiteur est administrateur. » | Seulement sur une écoute locale (`127.0.0.1`), pour un essai. |
| « Comptes locaux » | Les comptes créés dans ES.Topo. | Petite équipe, ou pas d'annuaire. |
| « LDAP » | Les personnes de l'annuaire qui appartiennent à un groupe configuré, **et** les comptes locaux. | Équipe gérée dans un annuaire OpenLDAP. |

Le mode par défaut est « Aucune ». Pour en changer : choisissez le « Mode », puis « Enregistrer ». Le changement est immédiat.

Passer à « Comptes locaux » ou à « LDAP » exige deux choses, vérifiées à l'enregistrement :

1. un transport sûr (section suivante) ;
2. au moins un **compte local administrateur actif**. Sinon l'enregistrement est refusé avec le message `create a local admin account before enabling authentication (the way back in if the directory fails)`. Créez donc le compte d'abord, changez le mode ensuite.

## La règle de transport

Un mot de passe ne doit pas circuler en clair. Les modes « Comptes locaux » et « LDAP » ne sont acceptés que dans l'un de ces cas :

- ES.Topo sert HTTPS (`ESTOPO_TLS_CERT` et `ESTOPO_TLS_KEY`) ;
- ES.Topo n'écoute que localement (`ESTOPO_LISTEN=127.0.0.1:…` ou `localhost:…`) ;
- `ESTOPO_ALLOW_INSECURE_AUTH=true` est défini. Un bandeau s'affiche alors, y compris sur l'écran de connexion : « Attention : les identifiants circulent en clair (HTTP sans TLS, ESTOPO_ALLOW_INSECURE_AUTH). ». À réserver à un réseau de test.

La règle est vérifiée à deux moments : au changement de mode (refus avec le code `insecure_transport`), et au démarrage du service (le démarrage est refusé).

## Comptes locaux

### Rôles

| Rôle | Peut |
|---|---|
| « lecteur » | consulter tous les écrans et exporter |
| « admin » | en plus : tout ce qui se trouve sous **Administration** (clusters, rétention, comptes, haute disponibilité…) |

### Créer, modifier, désactiver, supprimer

Sous « Utilisateurs » :

- **Créer** : saisissez un « Nom », choisissez le rôle, saisissez un mot de passe, puis « Enregistrer ». Le nom ne peut contenir ni espace, ni `/`, ni `\`.
- **Modifier** : cliquez sur ✎ dans la ligne du compte ; le formulaire se remplit. Changez le rôle, cochez « Désactivé » ou saisissez un nouveau mot de passe (« Mot de passe (vide = inchangé) »), puis « Enregistrer ».
- **Supprimer** : bouton « Supprimer » de la ligne. On ne peut pas supprimer son propre compte.

Un nouveau mot de passe, un changement de rôle ou la désactivation **ferment les sessions ouvertes** du compte.

À la connexion, le nom d'un compte local est comparé sans tenir compte des majuscules : `Alice` et `alice` désignent le même compte.

La colonne « Source » indique `local` ou `ldap`. Les comptes `ldap` sont créés automatiquement à la première connexion d'une personne de l'annuaire ; leur rôle est relu dans l'annuaire à chaque connexion. Vous pouvez désactiver un compte `ldap` : il le reste, même si l'annuaire l'accepte.

### Règle de mot de passe

Une seule règle : **12 caractères au moins**. Les mots de passe sont enregistrés sous forme d'empreinte argon2id, jamais en clair.

### Changer son propre mot de passe

Cliquez sur votre nom, en haut à droite : la fenêtre « Changer mon mot de passe » demande le « Mot de passe actuel » et le « Nouveau mot de passe (12 caractères min.) ». Après « Enregistrer », le message « Mot de passe changé. Vos autres sessions ont été fermées. » s'affiche ; la session en cours reste ouverte. Seuls les comptes locaux ont un mot de passe dans ES.Topo : celui d'un compte de l'annuaire se change dans l'annuaire.

### Réinitialiser un mot de passe en ligne de commande

Quand plus aucun administrateur ne peut se connecter, donnez un nouveau mot de passe à un compte local depuis la machine du service, avec le même utilisateur système et le même dossier de données que le service :

```bash
sudo -u estopo env ESTOPO_DATA_DIR=/var/lib/estopo /opt/estopo/estopo admin reset-password admin
```

Résultat attendu : `New password for admin: …` (un mot de passe aléatoire, affiché une seule fois). La commande ferme les sessions du compte et inscrit l'action au journal d'audit. Elle peut être lancée pendant que le service tourne.

À savoir :

- le nom doit être écrit exactement comme dans la liste des utilisateurs ;
- la commande refuse un compte de l'annuaire (`account … is not a local account`) ;
- elle ne réactive pas un compte désactivé ;
- sur une instance standby, les comptes sont recopiés depuis le primaire : faites la réinitialisation sur le primaire.

### Le dernier administrateur local

Tant que l'authentification est activée, ES.Topo refuse de supprimer, de désactiver ou de rétrograder en lecteur le **dernier** compte local administrateur actif (code `last_admin`). Sans lui, personne ne pourrait plus administrer l'instance, ni y entrer si l'annuaire tombe.

## Sessions

- Après la connexion, le navigateur reçoit un cookie de session (`estopo_session`), inaccessible aux scripts de la page, limité au même site, et marqué `Secure` quand la page est servie en HTTPS. Il disparaît à la fermeture du navigateur.
- **Inactivité** : « Expiration des sessions inactives (heures) », 12 par défaut, de 1 à 720. L'échéance est repoussée au fil de l'usage, mais seulement quand il reste moins de la moitié du délai : en pratique, une session inactive expire entre la moitié du délai et le délai entier.
- **Durée maximale** : une session expire au plus tard **7 jours** après la connexion, même si elle est utilisée sans arrêt. Cette durée ne se règle pas.
- « Se déconnecter » ferme la session.
- Changer de mode d'authentification ferme toutes les sessions ouvertes, sauf celle de l'administrateur local qui fait le changement (quand le nouveau mode n'est pas « Aucune »).
- La langue et le thème choisis sont enregistrés avec le compte et le suivent d'un navigateur à l'autre.

## Limitation des tentatives de connexion

Les échecs sont comptés par fenêtre de 15 minutes :

- pour chaque couple **utilisateur + adresse du client**, et pour chaque **adresse du client** : à partir du cinquième échec, il faut attendre avant de réessayer. L'attente est de 2 secondes, puis double à chaque nouvel échec, jusqu'à 15 minutes ;
- pour chaque **utilisateur, toutes adresses confondues** : même mécanisme à partir du cinquantième échec, pour freiner une attaque répartie sur un seul compte.

Une connexion réussie remet les compteurs à zéro. Pendant l'attente, l'écran affiche « Trop de tentatives, réessayez dans quelques minutes. ». Un annuaire injoignable n'est pas compté comme un échec. Les compteurs sont en mémoire : un redémarrage les efface. La vérification du mot de passe actuel, lors d'un changement de mot de passe, est soumise à la même limitation.

## LDAP

Seul **OpenLDAP** est testé (en LDAPS et en StartTLS). Les filtres sont réglables, mais le fonctionnement avec un autre annuaire, Active Directory compris, n'a pas été vérifié.

### Principe

À chaque connexion d'une personne qui n'a pas de compte local :

1. ES.Topo se connecte à l'annuaire, en TLS, et s'identifie avec un **compte de service** ;
2. il cherche la personne avec le filtre utilisateur ; il doit trouver exactement une entrée ;
3. il vérifie le mot de passe en s'identifiant comme cette personne (*bind*) ;
4. il cherche les groupes de la personne et en déduit son rôle.

Un mot de passe vide est refusé avant tout appel à l'annuaire. Les noms sont ramenés en minuscules.

### Les champs

Choisissez le mode « LDAP » : la carte « Annuaire LDAP » apparaît.

| Champ | Signification | Défaut |
|---|---|---|
| « URL des serveurs (une par ligne, essayées dans l'ordre ; ldaps:// ou ldap:// avec StartTLS) » | Adresses des serveurs. Le LDAP en clair est refusé. | obligatoire |
| « StartTLS (URL ldap://) » | À cocher pour des URL `ldap://` : la connexion passe en TLS avant toute identification. | décoché |
| « Fichier de la CA (PEM ; vide = CA système) » | CA qui a signé le certificat de l'annuaire. Un fichier, pas un dossier. | CA du système |
| « Nom TLS attendu (si différent de l'hôte) » | Nom à vérifier dans le certificat quand il diffère du nom d'hôte de l'URL. | nom d'hôte de l'URL |
| « DN du compte de service » | Compte avec lequel ES.Topo cherche les personnes et les groupes. Un droit de lecture suffit. | obligatoire |
| « Fichier du mot de passe du compte de service » | Chemin du fichier qui contient ce mot de passe. Il est relu à chaque connexion : pas de redémarrage après une rotation. | obligatoire |
| « Base de recherche des utilisateurs » | Branche où chercher les personnes (sous-arbre entier). | obligatoire |
| « Filtre utilisateur ({user} = identifiant saisi) » | Filtre de recherche ; `{user}` est remplacé par le nom saisi, protégé contre l'injection. | `(&(objectClass=inetOrgPerson)(uid={user}))` |
| « Attribut du nom de compte (lu dans l'entrée trouvée) » | Attribut de l'entrée trouvée qui nomme le compte dans ES.Topo ; il doit avoir une seule valeur. Avec un filtre qui accepte l'identifiant ou l'adresse, `(\|(uid={user})(mail={user}))`, se connecter avec l'une ou l'autre ouvre le même compte. Pour Active Directory : `sAMAccountName`. | `uid` |
| « Base de recherche des groupes » | Branche où chercher les groupes. | obligatoire |
| « Filtre de groupe ({dn} = DN de l'utilisateur, {user} = nom de compte) » | Filtre qui trouve les groupes d'une personne. | groupes `groupOfNames` (attribut `member`), `groupOfUniqueNames` (`uniqueMember`) et `posixGroup` (`memberUid`) |
| « Groupes administrateurs (DN, un par ligne) » | Appartenir à l'un d'eux donne le rôle admin. | au moins un groupe, admin ou lecteur |
| « Groupes lecteurs (DN, un par ligne) » | Appartenir à l'un d'eux donne le rôle lecteur. | |

Le délai d'attente de l'annuaire est de 10 secondes ; il ne se règle pas dans l'interface. Si `ESTOPO_SECRETS_DIR` est défini, le fichier de la CA et celui du mot de passe doivent se trouver dans ce dossier.

### Exemple complet avec OpenLDAP

Annuaire d'exemple :

```
dc=example,dc=org
├── ou=services
│   └── cn=estopo                  compte de service
├── ou=people
│   ├── uid=alice                  inetOrgPerson
│   └── uid=bob                    inetOrgPerson
└── ou=groups
    ├── cn=estopo-admins           groupOfNames, member: uid=alice,ou=people,dc=example,dc=org
    └── cn=estopo-readers          groupOfNames, member: uid=bob,ou=people,dc=example,dc=org
```

1. Sur la machine d'ES.Topo, placez la CA de l'annuaire et le mot de passe du compte de service :

   ```bash
   install -o root -g estopo -m 0640 ldap-ca.pem /etc/estopo/secrets/ldap-ca.pem
   install -o root -g estopo -m 0640 ldap-bind-password /etc/estopo/secrets/ldap-bind-password
   ```

2. Vérifiez qu'un compte local administrateur existe (section [Comptes locaux](#comptes-locaux)).
3. Dans **Administration › Authentification**, choisissez le mode « LDAP » et remplissez :

   | Champ | Valeur |
   |---|---|
   | URL des serveurs | `ldaps://ldap.example.org:636` |
   | Fichier de la CA | `/etc/estopo/secrets/ldap-ca.pem` |
   | DN du compte de service | `cn=estopo,ou=services,dc=example,dc=org` |
   | Fichier du mot de passe du compte de service | `/etc/estopo/secrets/ldap-bind-password` |
   | Base de recherche des utilisateurs | `ou=people,dc=example,dc=org` |
   | Filtre utilisateur | *(vide : filtre par défaut)* |
   | Attribut du nom de compte | *(vide : `uid`)* |
   | Base de recherche des groupes | `ou=groups,dc=example,dc=org` |
   | Filtre de groupe | *(vide : filtre par défaut)* |
   | Groupes administrateurs | `cn=estopo-admins,ou=groups,dc=example,dc=org` |
   | Groupes lecteurs | `cn=estopo-readers,ou=groups,dc=example,dc=org` |

4. Testez avant d'enregistrer : saisissez `alice` et son mot de passe dans les deux champs au-dessus du bouton « Tester », puis cliquez. Résultat attendu : six étapes cochées et « Authentification réussie : rôle admin. ».
5. Cliquez sur « Enregistrer » (en haut de la page). Déconnectez-vous et connectez-vous comme `alice`.

### Le bouton « Tester »

Le test porte sur les valeurs **saisies**, même non enregistrées, et n'enregistre rien. Sans utilisateur, il ne vérifie que la connexion et le compte de service (« Connexion et compte de service OK (saisir un utilisateur pour tester une authentification complète). »). Avec un utilisateur et son mot de passe, il déroule une authentification complète et affiche chaque étape :

| Étape | Ce qui est vérifié | Si elle échoue |
|---|---|---|
| « Connexion TLS » | joindre un serveur et établir TLS | adresse, port, pare-feu, CA, nom dans le certificat |
| « Bind du compte de service » | DN et mot de passe du compte de service | DN, contenu ou droits du fichier de mot de passe |
| « Recherche de l'utilisateur » | exactement une entrée trouvée | base de recherche, filtre ; `no single matching entry` = aucune ou plusieurs entrées |
| « Vérification du mot de passe » | bind avec le mot de passe saisi | mot de passe de la personne |
| « Recherche des groupes » | liste des groupes trouvés | base de recherche des groupes, filtre |
| « Rôle » | rôle obtenu | `member of no configured group` = la personne n'est dans aucun groupe configuré |

Le test est inscrit au journal d'audit. Il est refusé sur une instance standby.

### Des groupes aux rôles

- Appartenir à un groupe administrateur donne le rôle admin ; sinon, appartenir à un groupe lecteur donne le rôle lecteur ; sinon la connexion est **refusée**.
- Les DN sont comparés à la manière de LDAP : sans tenir compte des majuscules ni des espaces autour des virgules.
- Avec les filtres par défaut, seule l'**appartenance directe** compte : un groupe membre d'un autre groupe n'est pas suivi.
- Le rôle est relu à chaque connexion. S'il a changé, les sessions ouvertes avec l'ancien rôle sont fermées.

### Garder un compte local de secours

En mode « LDAP », les comptes locaux continuent de fonctionner : quand le nom saisi est celui d'un compte local, l'annuaire n'est pas consulté. C'est la porte d'entrée quand l'annuaire est en panne ou mal configuré. Gardez donc au moins un compte local administrateur, avec un mot de passe conservé en lieu sûr ; ES.Topo vous empêche de supprimer le dernier.

Conséquence : une personne de l'annuaire dont le nom est aussi celui d'un compte local ne peut pas se connecter avec son mot de passe d'annuaire. Donnez aux comptes locaux des noms qui n'existent pas dans l'annuaire (par exemple `secours-admin`).

Quand l'annuaire est injoignable, l'écran de connexion affiche « Annuaire indisponible, réessayez plus tard ou utilisez un compte local. ».

## Derrière un proxy inverse

- **En-tête `Host`** : le proxy doit transmettre l'en-tête `Host` reçu du navigateur. ES.Topo refuse toute modification, et la connexion elle-même, quand l'origine annoncée par le navigateur ne correspond pas à cet en-tête (code `cross_site`, message `cross-site request refused: the origin is not this host`).
- **Adresse du client** : ES.Topo ne lit pas `X-Forwarded-For`. Derrière un proxy, toutes les connexions viennent de l'adresse du proxy : la limite par adresse est donc **partagée par tous les utilisateurs**, et quelques échecs de l'un retardent les autres.
- **Règle de transport** : elle porte sur l'écoute d'ES.Topo, pas sur celle du proxy. Si le proxy est sur la même machine, faites écouter ES.Topo sur `127.0.0.1`. S'il est sur une autre machine, servez HTTPS aussi entre le proxy et ES.Topo.
- **Cookie `Secure`** : quand le proxy termine HTTPS, le cookie est tout de même marqué `Secure`, d'après l'origine `https://` annoncée par le navigateur.
- ES.Topo doit être servi à la racine du site : il n'y a pas de réglage de préfixe de chemin.

## Revenir au mode sans authentification

Choisissez le mode « Aucune (tout le monde est admin) » et enregistrez. Tout visiteur redevient administrateur ; les comptes sont conservés et resserviront si vous réactivez un mode. Ne le faites que sur une écoute locale.

## Dépannage

| Symptôme | Cause et remède |
|---|---|
| À l'enregistrement du mode : `authentication mode "local" over plain HTTP on a non-loopback address is refused…` | Voir [La règle de transport](#la-règle-de-transport). |
| À l'enregistrement du mode : `create a local admin account before enabling authentication…` | Créez d'abord un compte local de rôle admin. |
| `the last local admin account cannot be disabled or demoted` (ou `deleted`) | Créez un autre administrateur local avant de toucher à celui-ci. |
| `the password must have at least 12 characters` | Allongez le mot de passe. |
| « Identifiants invalides. » | Nom ou mot de passe erroné, compte désactivé, ou personne de l'annuaire hors des groupes configurés. Le détail n'est volontairement pas donné ; utilisez le bouton « Tester » en tant qu'administrateur. |
| « Trop de tentatives, réessayez dans quelques minutes. » | Voir [Limitation des tentatives](#limitation-des-tentatives-de-connexion). |
| « Annuaire indisponible… » | Annuaire injoignable, certificat refusé ou compte de service refusé. Le journal du service contient `LDAP authentication unavailable` avec la raison. Connectez-vous avec un compte local. |
| `… not an existing file of the secrets directory (ESTOPO_SECRETS_DIR)` | Le fichier de CA ou de mot de passe LDAP est hors du dossier des secrets. |
| Toute modification échoue avec `cross-site request refused…` | Proxy inverse qui change l'en-tête `Host`. |
| Réponse 503 `authentication is unavailable, retry later` | ES.Topo n'a pas pu lire ses réglages d'authentification ou ses sessions (base indisponible). Il refuse alors la requête plutôt que de laisser entrer. Consultez le journal du service. |
| Plus aucun administrateur ne peut entrer | [Réinitialisez un mot de passe en ligne de commande](#réinitialiser-un-mot-de-passe-en-ligne-de-commande). |
| Modification refusée : `this instance is standby: changes are made on the primary` | Les comptes et le mode se gèrent sur le primaire. |
