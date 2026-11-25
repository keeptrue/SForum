FROM golang:1.26.11-alpine AS base

ARG SFORUM_BUILTIN_BUILD_JOBS=4

WORKDIR /app/apps/api
RUN apk add --no-cache ca-certificates git

COPY apps/api/go.mod apps/api/go.sum* ./
RUN go mod download

COPY apps/api/ ./
COPY extensions/builtin /app/extensions/builtin
# 内置插件后端二进制（与 scripts/build-builtin-plugins.sh 对齐）：
# 受保护插件均构建后刷新 V3 packageFiles digest 并跑宿主契约门禁。
# CLI 只编译一次；插件 build/digest/validate/test 受控并发，失败即取消同批任务。
RUN set -eu; \
	jobs="${SFORUM_BUILTIN_BUILD_JOBS:-4}"; \
	case "$jobs" in \
		''|*[!0-9]*|0) echo "SFORUM_BUILTIN_BUILD_JOBS must be a positive integer" >&2; exit 2 ;; \
	esac; \
	mkdir -p /tmp/sforum-builtins; \
	cd /app/apps/api; \
	CGO_ENABLED=0 go build -trimpath -buildvcs=false -o /usr/local/bin/sforum ./cmd/sforum; \
	build_builtin() { \
		id="$1"; \
		validate="$2"; \
		pkg="/app/extensions/builtin/plugins/$id"; \
		( \
			set -eu; \
			cd "$pkg/backend"; \
			CGO_ENABLED=0 GOOS=linux go build -trimpath -buildvcs=false -ldflags="-s -w" -o plugin .; \
			cd /app/apps/api; \
			sforum extension digest --write "$pkg"; \
			if [ "$validate" = "1" ]; then sforum extension validate "$pkg"; fi; \
			sforum extension test "$pkg"; \
		) >"/tmp/sforum-builtins/$id.log" 2>&1; \
	}; \
	wait_batch() { \
		remaining="$*"; \
		while [ -n "$remaining" ]; do \
			next=""; \
			for pid in $remaining; do \
				if kill -0 "$pid" 2>/dev/null; then \
					next="$next $pid"; \
				elif ! wait "$pid"; then \
					for other in $remaining; do \
						[ "$other" = "$pid" ] || kill "$other" 2>/dev/null || true; \
					done; \
					for other in $remaining; do \
						wait "$other" 2>/dev/null || true; \
					done; \
					for log in /tmp/sforum-builtins/*.log; do \
						[ -e "$log" ] && cat "$log"; \
					done; \
					return 1; \
				fi; \
			done; \
			remaining="${next# }"; \
			[ -z "$remaining" ] || sleep 0.2; \
		done; \
	}; \
	run_all() { \
		while [ "$#" -gt 0 ]; do \
			batch=""; \
			count=0; \
			while [ "$count" -lt "$jobs" ] && [ "$#" -gt 0 ]; do \
				job="$1"; shift; \
				build_builtin "${job%:*}" "${job##*:}" & \
				batch="$batch $!"; \
				count=$((count + 1)); \
			done; \
			wait_batch $batch; \
		done; \
	}; \
	run_all \
		sforum-smtp:0 \
		sforum-content-policy:1 \
		sforum-storage-fs:0 \
		sforum-storage-s3:0 \
		sforum-search-site:0 \
		sforum-auth-github:0 \
		sforum-web-push:0 \
		sforum-shortcodes:0

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

FROM golang:1.26.11-alpine AS migrate-build

WORKDIR /app/apps/api
RUN apk add --no-cache ca-certificates

COPY apps/api/go.mod apps/api/go.sum* ./
RUN go mod download

COPY apps/api/ ./
ARG SFORUM_VERSION=dev
ARG SFORUM_COMMIT=
ARG SFORUM_BUILD_TIME=
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -buildvcs=false \
	-ldflags="-s -w -X github.com/keeptrue/sforum/apps/api/version.Current=${SFORUM_VERSION} -X github.com/keeptrue/sforum/apps/api/version.Commit=${SFORUM_COMMIT} -X github.com/keeptrue/sforum/apps/api/version.BuildTime=${SFORUM_BUILD_TIME}" \
	-o /out/sforum-migrate ./cmd/migrate

FROM alpine:3.24 AS api

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

FROM alpine:3.24 AS migrate

RUN apk add --no-cache ca-certificates && \
	addgroup -S sforum && adduser -S -G sforum sforum
LABEL io.sforum.migrations.online-safe-check="v1"
WORKDIR /app
COPY --from=migrate-build /out/sforum-migrate /usr/local/bin/sforum-migrate
USER sforum
CMD ["sforum-migrate"]
