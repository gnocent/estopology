#!/usr/bin/env bash
# Generates a test PKI shaped like an enterprise one: root CA -> intermediate
# CA -> leaf certificates (ES nodes, estopo client). Test material only.
# Usage: pki.sh <output dir>   (skips generation if the directory is complete)
set -euo pipefail
out=${1:?output dir}
mkdir -p "$out"
cd "$out"
[[ -f client.key && -f node.key ]] && exit 0

subj="/O=ES.Topo test/OU=run"
days=3650

openssl req -x509 -newkey rsa:2048 -nodes -keyout root.key -out root.crt -days $days \
  -subj "$subj/CN=Test Root CA" \
  -addext "basicConstraints=critical,CA:TRUE" -addext "keyUsage=critical,keyCertSign,cRLSign" 2>/dev/null

openssl req -newkey rsa:2048 -nodes -keyout inter.key -out inter.csr -subj "$subj/CN=Test Intermediate CA" 2>/dev/null
openssl x509 -req -in inter.csr -CA root.crt -CAkey root.key -CAcreateserial -out inter.crt -days $days \
  -extfile <(printf "basicConstraints=critical,CA:TRUE,pathlen:0\nkeyUsage=critical,keyCertSign,cRLSign\n") 2>/dev/null

leaf() { # name cn extensions
  openssl req -newkey rsa:2048 -nodes -keyout "$1.tmp.key" -out "$1.csr" -subj "$subj/CN=$2" 2>/dev/null
  openssl x509 -req -in "$1.csr" -CA inter.crt -CAkey inter.key -CAcreateserial -out "$1.leaf.crt" -days $days \
    -extfile <(printf "%s" "$3") 2>/dev/null
  openssl pkcs8 -topk8 -nocrypt -in "$1.tmp.key" -out "$1.key"
  cat "$1.leaf.crt" inter.crt > "$1.crt" # full chain, as enterprises deliver them
  rm -f "$1.tmp.key" "$1.csr" "$1.leaf.crt"
}

leaf node "es-node" "basicConstraints=CA:FALSE
keyUsage=critical,digitalSignature,keyEncipherment
extendedKeyUsage=serverAuth,clientAuth
subjectAltName=DNS:es01,DNS:es02,DNS:es03,DNS:es04,DNS:localhost,IP:127.0.0.1
"
leaf client "estopo" "basicConstraints=CA:FALSE
keyUsage=critical,digitalSignature,keyEncipherment
extendedKeyUsage=clientAuth
"
cat inter.crt root.crt > ca-chain.crt
rm -f ./*.csr ./*.srl
chmod 644 ./*.crt ./*.key
echo "PKI generated in $out"
