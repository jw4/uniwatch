FROM golang AS certs

FROM scratch

COPY --from=certs /usr/share/zoneinfo /usr/share/zoneinfo
COPY --from=certs /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY uniwatch uniwatch

ENTRYPOINT ["/uniwatch"]
