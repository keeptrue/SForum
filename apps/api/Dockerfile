FROM golang:1.26.10-alpine AS base

WORKDIR /app/apps/api
RUN apk add --no-cache ca-certificates git

COPY apps/api/go.mod apps/api/go.sum* ./
RUN go mod download

COPY apps/api/ ./
COPY extensions/builtin /app/extensions/builtin
# 内置插件后端二进制（与 scripts/build-builtin-plugins.sh 对齐）：
# 受保护插件均构建后刷新 V3 packageFiles digest 并跑宿主契约门禁。
RUN cd /app/extensions/builtin/plugins/sforum-smtp/backend && \
	CGO_ENABLED=0 GOOS=linux go build -trimpath -buildvcs=false -ldflags="-s -w" -o plugin . && \
	cd /app/extensions/builtin/plugins/sforum-content-policy/backend && \
	CGO_ENABLED=0 GOOS=linux go build -trimpath -buildvcs=false -ldflags="-s -w" -o plugin . && \
	cd /app/extensions/builtin/plugins/sforum-storage-fs/backend && \
	CGO_ENABLED=0 GOOS=linux go build -trimpath -buildvcs=false -ldflags="-s -w" -o plugin . && \
	cd /app/extensions/builtin/plugins/sforum-storage-s3/backend && \
	CGO_ENABLED=0 GOOS=linux go build -trimpath -buildvcs=false -ldflags="-s -w" -o plugin . && \
	cd /app/extensions/builtin/plugins/sforum-search-site/backend && \
	CGO_ENABLED=0 GOOS=linux go build -trimpath -buildvcs=false -ldflags="-s -w" -o plugin . && \
	cd /app/extensions/builtin/plugins/sforum-auth-github/backend && \
	CGO_ENABLED=0 GOOS=linux go build -trimpath -buildvcs=false -ldflags="-s -w" -o plugin . && \
	cd /app/extensions/builtin/plugins/sforum-web-push/backend && \
	CGO_ENABLED=0 GOOS=linux go build -trimpath -buildvcs=false -ldflags="-s -w" -o plugin . && \
	cd /app/extensions/builtin/plugins/sforum-shortcodes/backend && \
	CGO_ENABLED=0 GOOS=linux go build -trimpath -buildvcs=false -ldflags="-s -w" -o plugin . && \
	cd /app/apps/api && \
	CGO_ENABLED=0 go run ./cmd/sforum extension digest --write /app/extensions/builtin/plugins/sforum-smtp && \
	CGO_ENABLED=0 go run ./cmd/sforum extension test /app/extensions/builtin/plugins/sforum-smtp && \
	CGO_ENABLED=0 go run ./cmd/sforum extension digest --write /app/extensions/builtin/plugins/sforum-content-policy && \
	CGO_ENABLED=0 go run ./cmd/sforum extension validate /app/extensions/builtin/plugins/sforum-content-policy && \
	CGO_ENABLED=0 go run ./cmd/sforum extension test /app/extensions/builtin/plugins/sforum-content-policy && \
	CGO_ENABLED=0 go run ./cmd/sforum extension digest --write /app/extensions/builtin/plugins/sforum-storage-fs && \
	CGO_ENABLED=0 go run ./cmd/sforum extension test /app/extensions/builtin/plugins/sforum-storage-fs && \
	CGO_ENABLED=0 go run ./cmd/sforum extension digest --write /app/extensions/builtin/plugins/sforum-storage-s3 && \
	CGO_ENABLED=0 go run ./cmd/sforum extension test /app/extensions/builtin/plugins/sforum-storage-s3 && \
	CGO_ENABLED=0 go run ./cmd/sforum extension digest --write /app/extensions/builtin/plugins/sforum-search-site && \
	CGO_ENABLED=0 go run ./cmd/sforum extension test /app/extensions/builtin/plugins/sforum-search-site && \
	CGO_ENABLED=0 go run ./cmd/sforum extension digest --write /app/extensions/builtin/plugins/sforum-auth-github && \
	CGO_ENABLED=0 go run ./cmd/sforum extension test /app/extensions/builtin/plugins/sforum-auth-github && \
	CGO_ENABLED=0 go run ./cmd/sforum extension digest --write /app/extensions/builtin/plugins/sforum-web-push && \
	CGO_ENABLED=0 go run ./cmd/sforum extension test /app/extensions/builtin/plugins/sforum-web-push && \
	CGO_ENABLED=0 go run ./cmd/sforum extension digest --write /app/extensions/builtin/plugins/sforum-shortcodes && \
	CGO_ENABLED=0 go run ./cmd/sforum extension test /app/extensions/builtin/plugins/sforum-shortcodes

FROM base AS dev

RUN go install github.com/air-verse/air@latest
CMD ["air", "-c", ".air.toml"]

FROM base AS build

ARG SFORUM_VERSION=dev
ARG SFORUM_COMMIT=
ARG SFORUM_BUILD_TIME=
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -buildvcs=false \
	-ldflags="-s -w -X github.com/keeptrue/sforum/apps/api/version.Current=${SFORUM_VERSION} -X github.com/keeptrue/sforum/apps/api/version.Commit=${SFORUM_COMMIT} -X github.com/keeptrue/sforum/apps/api/version.BuildTime=${SFORUM_BUILD_TIME}" \
	-o /out/sforum-api ./cmd/api
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -buildvcs=false \
	-ldflags="-s -w -X github.com/keeptrue/sforum/apps/api/version.Current=${SFORUM_VERSION} -X github.com/keeptrue/sforum/apps/api/version.Commit=${SFORUM_COMMIT} -X github.com/keeptrue/sforum/apps/api/version.BuildTime=${SFORUM_BUILD_TIME}" \
	-o /out/sforum-worker ./cmd/worker
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -buildvcs=false \
	-ldflags="-s -w -X github.com/keeptrue/sforum/apps/api/version.Current=${SFORUM_VERSION} -X github.com/keeptrue/sforum/apps/api/version.Commit=${SFORUM_COMMIT} -X github.com/keeptrue/sforum/apps/api/version.BuildTime=${SFORUM_BUILD_TIME}" \
	-o /out/sforum-migrate ./cmd/migrate

FROM alpine:3.22 AS api

RUN apk add --no-cache ca-certificates && \
	# H6：容器以非 root 用户运行，RCE/路径穿越后权限受限。
	addgroup -S sforum && adduser -S -G sforum sforum && \
	mkdir -p /app/storage/app/attachments /var/lib/sforum/extensions && \
	chown -R sforum:sforum /app /var/lib/sforum
WORKDIR /app
COPY --from=build --chown=sforum:sforum /out/sforum-api /usr/local/bin/sforum-api
COPY --from=build --chown=sforum:sforum /app/extensions/builtin /app/extensions/builtin
USER sforum
EXPOSE 8080
CMD ["sforum-api"]

FROM alpine:3.22 AS worker

RUN apk add --no-cache ca-certificates && \
	addgroup -S sforum && adduser -S -G sforum sforum && \
	mkdir -p /app/storage/app/attachments /var/lib/sforum/extensions && \
	chown -R sforum:sforum /app /var/lib/sforum
WORKDIR /app
COPY --from=build --chown=sforum:sforum /out/sforum-worker /usr/local/bin/sforum-worker
COPY --from=build --chown=sforum:sforum /app/extensions/builtin /app/extensions/builtin
USER sforum
CMD ["sforum-worker"]

FROM alpine:3.22 AS migrate

RUN apk add --no-cache ca-certificates && \
	addgroup -S sforum && adduser -S -G sforum sforum
LABEL io.sforum.migrations.online-safe-check="v1"
WORKDIR /app
COPY --from=build /out/sforum-migrate /usr/local/bin/sforum-migrate
USER sforum
CMD ["sforum-migrate"]
