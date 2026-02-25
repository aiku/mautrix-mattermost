FROM matrixdotorg/synapse:latest

USER root

# Install curl for healthcheck
RUN apt-get update && apt-get install -y --no-install-recommends curl && \
    rm -rf /var/lib/apt/lists/*

# Appservice registration files (tokens must match bridge + agent configs)
RUN mkdir -p /appservices /data && chown -R 991:991 /appservices /data
COPY bridge-registration.yaml /appservices/bridge.yaml
COPY agent-registration.yaml /appservices/agents.yaml

COPY entrypoint.sh /custom-entrypoint.sh
RUN chmod +x /custom-entrypoint.sh

USER 991:991

ENTRYPOINT ["/custom-entrypoint.sh"]
