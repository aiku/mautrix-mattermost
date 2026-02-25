FROM alpine:3.21

RUN apk add --no-cache curl jq openssl bash

COPY init.sh /init.sh
COPY agents.conf /agents.conf
RUN chmod +x /init.sh

ENTRYPOINT ["/init.sh"]
