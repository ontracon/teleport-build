FROM debian:bookworm-slim@sha256:7c7b2c966bc9ee8cedfeef67e0e279108992c77681fa595db4a9d65c06ccc587
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates tzdata libpam0g libelf1 libssl3 libstdc++6 libatomic1 libpcsclite1 zlib1g \
    && rm -rf /var/lib/apt/lists/*
COPY bin/teleport bin/tctl bin/fdpass-teleport /usr/local/bin/
COPY LICENSE manifest.json /usr/local/share/teleport/
EXPOSE 3080
ENTRYPOINT ["/usr/local/bin/teleport"]
CMD ["start", "--config=/etc/teleport.yaml"]
