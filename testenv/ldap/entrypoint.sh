#!/bin/sh
# Hashes the test passwords ({SSHA}, like a real directory), loads the data
# and runs slapd in the foreground.
set -eu
cp /estopo/data.ldif /tmp/data.ldif
for pw in service-secret alice-password bob-password carol-password dave-password; do
  h=$(slappasswd -s "$pw")
  sed -i "s|@$pw@|$h|" /tmp/data.ldif
done
mkdir -p /var/lib/ldap
slapadd -f /estopo/slapd.conf -l /tmp/data.ldif
exec slapd -f /estopo/slapd.conf -h "ldap:/// ldaps:///" -d stats
