# The Banyan server: takes the register from the treasurer's desk and shows
# members the reports. The desktop app is not built here.

FROM node:24-bookworm-slim AS build
WORKDIR /src
RUN corepack enable
COPY package.json pnpm-lock.yaml pnpm-workspace.yaml ./
# Only what the server build needs is fetched: no Electron binary is downloaded.
RUN pnpm install --frozen-lockfile --ignore-scripts
COPY tsconfig.json vite.backend.config.ts ./
COPY src ./src
RUN pnpm build:backend

FROM node:24-bookworm-slim
ENV NODE_ENV=production \
    PORT=8322 \
    DATA_DIR=/data \
    BANYAN_ASSETS=/app
WORKDIR /app
COPY --from=build /src/out/backend ./out/backend
# The report pages are the desk's own frontend files, read as ordinary files.
COPY static ./static
COPY templates ./templates
COPY cheque-calibration.json package.json ./
# A fixed, unprivileged user. The data volume must be writable by 1000.
RUN mkdir -p /data && chown 1000:1000 /data
USER 1000:1000
VOLUME /data
EXPOSE 8322
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s \
  CMD node -e "fetch('http://127.0.0.1:'+process.env.PORT+'/healthz').then(r=>process.exit(r.ok?0:1),()=>process.exit(1))"
CMD ["node", "out/backend/index.mjs"]
