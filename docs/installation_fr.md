*(English version: [installation.md](installation.md))*

# Installer ES.Topo

Ce guide mène d'un téléchargement à un service qui tourne en HTTPS avec un compte administrateur. Il se suit de haut en bas. Les commandes sont pour Linux ; une section à la fin traite de Windows et de macOS.

ES.Topo est en version bêta (0.8.1-beta) : installez-le d'abord sur une machine de test.

Guides liés : [déclarer un cluster](clusters_fr.md), [authentification](authentication_fr.md), [haute disponibilité](high-availability_fr.md), [exploitation](operations_fr.md), [sécurité](../SECURITY_fr.md).

## Ce dont vous avez besoin

- Une machine Linux x86-64 qui peut joindre vos clusters Elasticsearch en HTTPS. Rien d'autre à installer : le binaire est statique.
- De l'espace disque pour l'historique. Ordre de grandeur mesuré sur des données simulées : environ 1 Go pour 30 jours d'historique de 373 000 copies de shards. Voir [Exploitation](operations_fr.md#dimensionnement).
- Pour une installation exposée sur le réseau : un certificat TLS et sa clé pour le nom de la machine.
- Les droits `root` pour créer l'utilisateur système et le service.

## Choisir et vérifier le binaire

1. Sur la [page des versions](../../../releases), téléchargez le binaire de votre plateforme et le fichier `SHA256SUMS` :

   | Plateforme | Fichier | Usage |
   |---|---|---|
   | Linux x86-64 | `estopo-linux-amd64` | production |
   | Windows x64 | `estopo-windows-amd64.exe` | essai, développement |
   | macOS arm64 (Apple Silicon) | `estopo-macos-arm64` | essai, développement |

   Le simulateur `essim` est fourni sous les mêmes noms (`essim-linux-amd64`, etc.). Il n'est pas nécessaire au fonctionnement d'ES.Topo.
2. Vérifiez la somme de contrôle, dans le dossier où se trouvent les deux fichiers :

   ```bash
   sha256sum -c SHA256SUMS --ignore-missing
   ```

   Résultat attendu : `estopo-linux-amd64: OK`. Si la commande affiche `FAILED`, le fichier est incomplet ou altéré : téléchargez-le de nouveau et ne l'exécutez pas.
3. Rendez le binaire exécutable et affichez sa version :

   ```bash
   chmod +x estopo-linux-amd64
   ./estopo-linux-amd64 version
   ```

   Résultat attendu : une ligne qui commence par `estopo 0.8.1-beta`.

Les binaires ne sont pas signés : la somme de contrôle prouve seulement que le fichier est celui qui a été publié.

## Premier essai à la main

Avant d'installer le service, vous pouvez lancer le binaire tel quel :

```bash
./estopo-linux-amd64 serve
```

Sans réglage, ES.Topo :

- écoute sur `127.0.0.1:8080`, en HTTP, joignable depuis cette machine seulement ;
- crée un dossier `data` à côté du binaire ;
- n'a pas d'authentification : tout visiteur est administrateur ;
- écrit ses journaux sur la sortie standard. La première ligne ressemble à `level=INFO msg=starting version=0.8.1-beta listen=127.0.0.1:8080 data_dir=… tls=false secrets_dir=""`.

Ouvrez `http://127.0.0.1:8080` dans un navigateur de la même machine. Pour arrêter, tapez Ctrl+C.

Le binaire accepte quatre commandes :

| Commande | Effet |
|---|---|
| `estopo serve` | démarre la collecte et l'interface web |
| `estopo version` | affiche la version |
| `estopo help` | affiche l'aide |
| `estopo admin reset-password <utilisateur>` | donne un nouveau mot de passe à un compte local (voir [Authentification](authentication_fr.md#réinitialiser-un-mot-de-passe-en-ligne-de-commande)) |

Il n'y a pas d'option en ligne de commande : les réglages de démarrage passent par des variables d'environnement, tout le reste se règle dans l'interface.

## Variables d'environnement

Elles sont lues au démarrage. Une valeur invalide empêche le démarrage, avec un message qui nomme la variable.

| Variable | Défaut | Effet |
|---|---|---|
| `ESTOPO_LISTEN` | `127.0.0.1:8080` | Adresse et port d'écoute. `0.0.0.0:8443` écoute sur toutes les interfaces. |
| `ESTOPO_DATA_DIR` | dossier `data` à côté du binaire | Dossier de données (historique, configuration, comptes). Créé s'il n'existe pas. |
| `ESTOPO_TLS_CERT`, `ESTOPO_TLS_KEY` | aucune | Certificat (chaîne complète acceptée) et clé privée, au format PEM. Les deux ensemble activent HTTPS. |
| `ESTOPO_ADMIN_PASSWORD` | aucune | Au tout premier démarrage seulement : crée le compte local `admin` avec ce mot de passe (12 caractères au moins) et active l'authentification locale. Voir [Sécuriser le premier démarrage](#sécuriser-le-premier-démarrage). |
| `ESTOPO_SECRETS_DIR` | aucune | Quand elle est définie, tous les chemins de fichiers saisis dans l'administration (certificats, clés, fichiers de mot de passe et de clé d'API des clusters, CA, fichiers LDAP) doivent se trouver dans ce dossier. Le dossier doit exister. |
| `ESTOPO_ALLOW_INSECURE_AUTH` | `false` | `true` autorise l'authentification, et le jeton de réplication, en HTTP clair sur une écoute non locale. À réserver à un réseau de test : les mots de passe circulent alors en clair. |
| `ESTOPO_LOG_LEVEL` | `info` | `debug`, `info`, `warn` ou `error`. |
| `ESTOPO_LOG_FORMAT` | `text` | `text` ou `json`. |
| `ESTOPO_REPLICATION_TOKEN_FILE` | aucune | Fichier du jeton partagé de la haute disponibilité (16 caractères au moins). Voir [Haute disponibilité](high-availability_fr.md). |
| `ESTOPO_STANDBY_OF` | aucune | URL du primaire : fait de cette instance une standby à son premier démarrage. |
| `ESTOPO_REPLICATION_CA` | CA du système | Fichier PEM de la CA qui a signé le certificat de l'autre instance. |
| `ESTOPO_PPROF_LISTEN` | aucune | Adresse locale (par exemple `127.0.0.1:6060`) du profileur Go, pour un diagnostic. Inutile en temps normal. |

## Installer comme service systemd

Le dépôt fournit deux fichiers d'exemple : [`deploy/estopo.service`](../deploy/estopo.service) et [`deploy/estopo.env.example`](../deploy/estopo.env.example). Téléchargez-les à côté du binaire, puis exécutez les étapes suivantes en tant que `root`.

1. Créez l'utilisateur système qui fera tourner le service :

   ```bash
   useradd --system --home-dir /var/lib/estopo --shell /sbin/nologin estopo
   ```

2. Installez le binaire sous le nom `estopo` :

   ```bash
   install -d -m 0755 /opt/estopo
   install -m 0755 estopo-linux-amd64 /opt/estopo/estopo
   ```

3. Créez le dossier de données, qui appartient au service, et les dossiers de configuration, que le service peut lire mais pas modifier :

   ```bash
   install -d -o estopo -g estopo -m 0750 /var/lib/estopo
   install -d -o root -g estopo -m 0750 /etc/estopo /etc/estopo/secrets /etc/estopo/tls
   ```

   `/etc/estopo/secrets` recevra les fichiers de secrets des clusters (voir [Déclarer un cluster](clusters_fr.md#les-fichiers-de-secrets)) ; `/etc/estopo/tls` recevra le certificat du service.
4. Installez le fichier d'environnement et l'unité :

   ```bash
   install -o root -g estopo -m 0640 estopo.env.example /etc/estopo/estopo.env
   install -m 0644 estopo.service /etc/systemd/system/estopo.service
   ```

5. Modifiez `/etc/estopo/estopo.env`. Le fichier d'exemple écoute sur toutes les interfaces en HTTPS (`ESTOPO_LISTEN=0.0.0.0:8443`) : il faut donc d'abord placer le certificat et la clé (section suivante). Pour un premier démarrage sans certificat, remplacez la ligne `ESTOPO_LISTEN` par `ESTOPO_LISTEN=127.0.0.1:8080` et mettez un `#` devant les deux lignes `ESTOPO_TLS_…`.
6. Démarrez le service et vérifiez-le :

   ```bash
   systemctl daemon-reload
   systemctl enable --now estopo
   systemctl status estopo
   journalctl -u estopo -n 20
   ```

   Résultat attendu : le service est `active (running)` et le journal contient une ligne `starting`. Si le service s'arrête aussitôt, la raison est dans le journal : voir [Dépannage](#dépannage).

L'unité d'exemple restreint le service : il ne peut écrire que dans `/var/lib/estopo`, et il ne voit pas les fichiers sous `/home` et `/root`. Placez donc les certificats et les secrets sous `/etc/estopo`, pas dans un dossier personnel.

## Activer HTTPS

HTTPS est nécessaire dès que le service est joignable depuis une autre machine avec l'authentification activée.

1. Obtenez auprès de votre autorité de certification un certificat pour le nom de la machine (par exemple `estopo.example.org`) et sa clé privée, au format PEM. Le fichier du certificat peut contenir la chaîne complète : le certificat du serveur d'abord, puis les certificats intermédiaires. La clé ne doit pas être protégée par une phrase de passe.
2. Copiez-les, lisibles par le groupe `estopo` seulement :

   ```bash
   install -o root -g estopo -m 0640 estopo-fullchain.pem /etc/estopo/tls/estopo-fullchain.pem
   install -o root -g estopo -m 0640 estopo.key /etc/estopo/tls/estopo.key
   ```

3. Dans `/etc/estopo/estopo.env` :

   ```sh
   ESTOPO_LISTEN=0.0.0.0:8443
   ESTOPO_TLS_CERT=/etc/estopo/tls/estopo-fullchain.pem
   ESTOPO_TLS_KEY=/etc/estopo/tls/estopo.key
   ```

4. Redémarrez et vérifiez depuis une autre machine :

   ```bash
   systemctl restart estopo
   curl https://estopo.example.org:8443/healthz
   ```

   Résultat attendu : `{"status":"ok","version":"0.8.1-beta","role":"primary","uptime":"…"}`. Si `curl` refuse le certificat, ajoutez `--cacert` suivi du fichier de votre CA. Ouvrez aussi le port dans le pare-feu de la machine si nécessaire.

À savoir :

- Le certificat et la clé sont lus au démarrage. **Après un renouvellement, redémarrez le service** (`systemctl restart estopo`).
- ES.Topo n'écoute que sur un seul port. Il n'y a pas de redirection de HTTP vers HTTPS.
- Quand ES.Topo sert lui-même HTTPS, il envoie l'en-tête `Strict-Transport-Security` : le navigateur refusera ensuite HTTP pour ce nom pendant un an.
- Pour placer un proxy inverse devant ES.Topo, voir [Exploitation](operations_fr.md#proxy-inverse).

## Sécuriser le premier démarrage

Sans authentification, tout visiteur est administrateur. Choisissez l'une des deux voies suivantes **avant** de rendre le service joignable par d'autres.

### Voie A : `ESTOPO_ADMIN_PASSWORD` au premier démarrage

C'est la voie la plus directe pour un service installé d'emblée en HTTPS.

1. Avant le tout premier démarrage (dossier de données vide), ajoutez dans `/etc/estopo/estopo.env`, avec HTTPS déjà configuré :

   ```sh
   ESTOPO_ADMIN_PASSWORD=une-phrase-de-passe-longue
   ```

   Le mot de passe doit avoir au moins 12 caractères. Dans un fichier d'environnement systemd, préférez des lettres, des chiffres et des tirets : les espaces, les guillemets, `#` et `$` y ont un sens particulier.
2. Démarrez le service. Le journal contient : `first start: account 'admin' created with ESTOPO_ADMIN_PASSWORD, local authentication enabled`.
3. Ouvrez l'interface : elle affiche l'écran « Connexion ». Connectez-vous avec l'utilisateur `admin` et ce mot de passe.
4. Supprimez la ligne `ESTOPO_ADMIN_PASSWORD` du fichier d'environnement : elle n'est plus utilisée. Changez ensuite le mot de passe dans l'interface (cliquez sur votre nom en haut à droite, « Changer mon mot de passe »), puisque l'ancien a séjourné dans un fichier.

Règles appliquées par le code :

- La variable n'agit que si aucun mode d'authentification n'a jamais été enregistré **et** qu'aucun compte n'existe. Ensuite elle est ignorée.
- Le démarrage est refusé si le mot de passe a moins de 12 caractères (même quand la variable serait ignorée).
- Le démarrage est refusé si le transport n'est pas sûr : il faut HTTPS, ou une écoute locale (`127.0.0.1`), ou `ESTOPO_ALLOW_INSECURE_AUTH=true`.
- Elle est sans effet sur une instance démarrée avec `ESTOPO_STANDBY_OF` : une standby reçoit ses comptes du primaire.

### Voie B : écoute locale, puis création d'un administrateur dans l'interface

1. Démarrez avec `ESTOPO_LISTEN=127.0.0.1:8080` (la valeur par défaut).
2. Depuis votre poste, ouvrez un tunnel SSH vers la machine, puis ouvrez `http://127.0.0.1:8080` dans votre navigateur :

   ```bash
   ssh -L 8080:127.0.0.1:8080 vous@estopo.example.org
   ```

3. Allez dans **Administration › Authentification**. Sous « Utilisateurs », saisissez un « Nom », choisissez le rôle « admin », saisissez un mot de passe d'au moins 12 caractères, puis cliquez sur « Enregistrer ».
4. En haut de la même page, choisissez le « Mode » « Comptes locaux » et cliquez sur « Enregistrer ». L'écran « Connexion » apparaît : connectez-vous avec le compte créé.
5. Configurez ensuite HTTPS et l'écoute réseau (section précédente), puis redémarrez.

Si vous passez à une écoute réseau sans HTTPS alors que l'authentification est activée, le service refuse de démarrer : c'est voulu.

Les modes d'authentification, les comptes et l'annuaire LDAP sont décrits dans [Authentification](authentication_fr.md).

## Ce que montre le premier écran

- **Sans authentification** : la page « Clusters », avec le bandeau « Authentification désactivée : tout visiteur est administrateur. » et le message « Aucun cluster configuré. Ajoutez-en un dans l'administration. ».
- **Avec authentification** : l'écran « Connexion » (champs « Utilisateur » et « Mot de passe », bouton « Se connecter »), puis la même page « Clusters ».

En haut à droite : la recherche, le thème, la langue (bouton « English »), le lien « Administration » (pour les administrateurs), votre nom et « Se déconnecter ». Chaque écran a un encadré « Comprendre cet écran ».

Étape suivante : [déclarer un premier cluster](clusters_fr.md).

## Windows et macOS

Ces binaires servent à essayer le produit et à développer. Aucune intégration comme service n'est fournie pour ces systèmes.

- **Les binaires ne sont pas signés.** Windows (SmartScreen) et macOS (Gatekeeper) peuvent afficher un avertissement ou bloquer le premier lancement d'un fichier téléchargé. Ce comportement n'a pas été vérifié en détail pour ES.Topo : suivez la procédure de votre système pour autoriser un programme que vous avez vous-même vérifié.
- **Vérifier la somme de contrôle** :
  - Windows (PowerShell) : `Get-FileHash .\estopo-windows-amd64.exe -Algorithm SHA256`, puis comparez la valeur avec la ligne correspondante de `SHA256SUMS` ;
  - macOS : `shasum -a 256 estopo-macos-arm64`, puis comparez de même.
- **Lancer** :
  - Windows (PowerShell) :

    ```powershell
    $env:ESTOPO_DATA_DIR = "C:\estopo\data"
    .\estopo-windows-amd64.exe serve
    ```

  - macOS :

    ```bash
    chmod +x estopo-macos-arm64
    ESTOPO_DATA_DIR="$HOME/estopo-data" ./estopo-macos-arm64 serve
    ```

Ouvrez ensuite `http://127.0.0.1:8080`. Ctrl+C arrête le programme.

## Mettre à jour

1. Lisez le [journal des versions](../CHANGELOG_fr.md) de la nouvelle version.
2. Sauvegardez le dossier de données, service arrêté (voir [Exploitation](operations_fr.md#sauvegarde-et-restauration)).
3. Remplacez le binaire et redémarrez :

   ```bash
   systemctl stop estopo
   install -m 0755 estopo-linux-amd64 /opt/estopo/estopo
   systemctl start estopo
   ```

À savoir :

- Au démarrage, ES.Topo met à jour lui-même la structure de ses bases de données quand la nouvelle version le demande. Cette opération ne se fait que vers l'avant : **revenir à une version plus ancienne n'est pas prévu**. Pour revenir en arrière, restaurez la sauvegarde.
- En haute disponibilité, les deux instances doivent tourner avec la même version : voir [Haute disponibilité](high-availability_fr.md#mettre-à-jour-les-deux-instances).
- La mise à jour de 0.8.1-beta vers 0.8.1-beta a été éprouvée sur un dossier de données simulé. Lisez d'abord les notes de mise à jour du [journal des versions](../CHANGELOG_fr.md#mise-à-jour-depuis-080-beta) (mode LDAP).

## Désinstaller

```bash
systemctl disable --now estopo
rm /etc/systemd/system/estopo.service
systemctl daemon-reload
rm -r /opt/estopo /etc/estopo
rm -r /var/lib/estopo        # supprime tout l'historique
userdel estopo
```

Côté Elasticsearch, supprimez le compte, la clé d'API ou la correspondance de rôle créés pour ES.Topo.

## Dépannage

Les messages de démarrage sont dans le journal (`journalctl -u estopo -n 50`) ou sur la sortie du terminal. Une erreur de configuration s'affiche sous `estopo: invalid configuration:` ; les autres sous `msg=fatal err="…"`. Dans les deux cas le programme s'arrête avec le code 1.

| Message | Signification et remède |
|---|---|
| `ESTOPO_LISTEN: "…": …` | L'adresse n'a pas la forme `hôte:port`. Exemples valides : `127.0.0.1:8080`, `0.0.0.0:8443`. |
| `ESTOPO_TLS_CERT and ESTOPO_TLS_KEY must be set together` | Une seule des deux variables est définie. |
| `ESTOPO_SECRETS_DIR: "…" is not a directory` | Le dossier n'existe pas. Créez-le. |
| `ESTOPO_ADMIN_PASSWORD must have at least 12 characters` | Allongez le mot de passe, ou supprimez la variable si un compte existe déjà. |
| `authentication mode "local" over plain HTTP on a non-loopback address is refused: …` (parfois précédé de `ESTOPO_ADMIN_PASSWORD:`) | L'authentification est activée et l'écoute n'est ni locale ni en HTTPS. Configurez `ESTOPO_TLS_CERT` et `ESTOPO_TLS_KEY`, ou écoutez sur `127.0.0.1`. |
| `replication over plain HTTP on a non-loopback address is refused: …` | Même règle pour le jeton de réplication. |
| `listen tcp …: bind: address already in use` | Le port est pris par un autre programme. Changez `ESTOPO_LISTEN`. |
| `listen tcp …: bind: permission denied` | Un port inférieur à 1024 demande des droits particuliers. Utilisez un port comme 8443. |
| `open /etc/estopo/tls/…: permission denied` ou `no such file or directory`, après la ligne `starting` | Le certificat ou la clé est absent, ou illisible par l'utilisateur `estopo`. Vérifiez le chemin, le propriétaire et les droits. |
| `tls: private key does not match public key` ou `tls: failed to find any PEM data…` | Le certificat et la clé ne vont pas ensemble, ou un fichier n'est pas au format PEM. |
| `create data directory: …` ou `unable to open database file` | Le dossier de données n'est pas accessible en écriture par l'utilisateur du service. |
| `authentication settings: …` | Le réglage d'authentification enregistré est illisible. ES.Topo refuse de démarrer plutôt que de se comporter comme s'il n'y avait pas d'authentification. Restaurez une sauvegarde du dossier de données. |

Autres situations :

- **La page ne s'ouvre pas depuis une autre machine.** L'écoute par défaut est locale. Vérifiez `ESTOPO_LISTEN` dans la ligne `starting` du journal, puis le pare-feu.
- **Le navigateur signale un certificat non fiable.** Le certificat n'est pas signé par une autorité que votre poste connaît, ou le fichier ne contient pas les certificats intermédiaires.
- **L'interface s'affiche mais toute modification échoue avec « cross-site request refused ».** Un proxy inverse modifie l'en-tête `Host` : voir [Exploitation](operations_fr.md#proxy-inverse).
- **Mot de passe administrateur perdu.** Voir [Authentification](authentication_fr.md#réinitialiser-un-mot-de-passe-en-ligne-de-commande).
