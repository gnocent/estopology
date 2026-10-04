# OpenLDAP for the LDAP tests of estopo (development only): the Debian slapd
# package on the official Debian image, no third-party image.
FROM debian:trixie-slim
RUN apt-get update \
 && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends slapd ldap-utils \
 && rm -rf /var/lib/apt/lists/* /var/lib/ldap/* /etc/ldap/slapd.d
COPY slapd.conf data.ldif entrypoint.sh /estopo/
EXPOSE 389 636
ENTRYPOINT ["/bin/sh", "/estopo/entrypoint.sh"]
