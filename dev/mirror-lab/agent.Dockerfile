FROM debian:bookworm-slim
ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get install -y --no-install-recommends \
    systemd systemd-sysv dbus ca-certificates curl sudo openssl iproute2 procps \
    libpam0g libelf1 libssl3 libstdc++6 libatomic1 libpcsclite1 zlib1g \
    && rm -rf /var/lib/apt/lists/*
COPY dev/mirror-lab/agent-entrypoint.sh /usr/local/sbin/lab-agent-entrypoint
RUN chmod 0755 /usr/local/sbin/lab-agent-entrypoint
ENTRYPOINT ["/usr/local/sbin/lab-agent-entrypoint"]
