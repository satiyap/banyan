# The Banyan server: takes the register from the treasurer's desk and shows
# members the reports. The desktop app is not built here.
#
# One static Go program with the report pages built in, on an empty image:
# no shell, no package manager, no interpreter.

FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
COPY assets.go cheque-calibration.json ./
COPY static ./static
COPY templates ./templates
COPY internal ./internal
COPY cmd/banyan-server ./cmd/banyan-server
# Only the modules the server uses are fetched; the desktop app's are not.
RUN CGO_ENABLED=0 GOFLAGS=-mod=mod go build -trimpath -ldflags="-w -s" -o /out/banyan-server ./cmd/banyan-server
# The data volume must be writable by the fixed user below.
RUN mkdir -p /out/data

FROM scratch
ENV PORT=8322 \
    DATA_DIR=/data \
    TMPDIR=/data/tmp
COPY --from=build /out/banyan-server /banyan-server
COPY --from=build --chown=1000:1000 /out/data /data
# A fixed, unprivileged user.
USER 1000:1000
VOLUME /data
EXPOSE 8322
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s \
  CMD ["/banyan-server", "healthcheck"]
ENTRYPOINT ["/banyan-server"]
