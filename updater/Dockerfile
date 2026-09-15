FROM python:3.13-alpine

RUN apk add --no-cache docker-cli docker-cli-compose

WORKDIR /opt/aivory-updater
COPY updater/updater.py ./updater.py

ENV AIVORY_UPDATER_LISTEN=0.0.0.0:8790 \
    AIVORY_UPDATER_STATE_DIR=/var/lib/aivory-updater \
    AIVORY_UPDATER_TOKEN_FILE=/shared/.aivory-update-token

EXPOSE 8790
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD wget -qO- http://127.0.0.1:8790/healthz >/dev/null 2>&1 || exit 1

ENTRYPOINT ["python", "/opt/aivory-updater/updater.py"]
