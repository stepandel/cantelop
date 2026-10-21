# Standalone image used only by check-starter.sh. Normal projects are built with
# the Cantelop CLI and do not need a Dockerfile unless they add native packages.
FROM node:22-alpine

WORKDIR /app
COPY .cantelop-qualification/session-runtime.mjs ./session-runtime.mjs

USER node
CMD ["node", "session-runtime.mjs"]
