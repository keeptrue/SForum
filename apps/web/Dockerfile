FROM oven/bun:1.4.4-alpine AS deps

WORKDIR /app/apps/web
COPY apps/web/package.json apps/web/bun.lock ./
COPY apps/web/packages/admin-sdk/package.json ./packages/admin-sdk/package.json
COPY apps/web/packages/plugin-ui/package.json ./packages/plugin-ui/package.json
RUN bun install --frozen-lockfile

FROM deps AS dev

COPY apps/web/ ./
COPY extensions/builtin /app/extensions/builtin
EXPOSE 3000
CMD ["bun", "run", "dev"]

FROM deps AS build

ARG SFORUM_VERSION=dev
ARG SFORUM_COMMIT=
ARG SFORUM_BUILD_TIME=
ENV NUXT_PUBLIC_SFORUM_VERSION=$SFORUM_VERSION
ENV NUXT_PUBLIC_SFORUM_COMMIT=$SFORUM_COMMIT
ENV NUXT_PUBLIC_SFORUM_BUILD_TIME=$SFORUM_BUILD_TIME
COPY apps/web/ ./
COPY extensions/builtin /app/extensions/builtin
RUN ln -sfn .nuxt-build .nuxt && bun run build

FROM oven/bun:1.4.4-alpine AS prod

# 升级 OpenSSL 运行时库以修复 CVE-2026-45447（libcrypto3/libssl3 3.5.6-r0 存在 HIGH
# 漏洞，Alpine 3.22 仓库已提供 3.5.7-r0 修复版本）。仅影响最终运行镜像，不引入 Node。
RUN apk add --no-cache --upgrade 'libcrypto3>=3.5.7-r0' 'libssl3>=3.5.7-r0'

# H6：容器以非 root 用户运行。
RUN addgroup -S sforum && adduser -S -G sforum sforum && \
	mkdir -p /app/apps/web && \
	chown -R sforum:sforum /app
WORKDIR /app/apps/web
ENV HOST=0.0.0.0
ENV PORT=3000
COPY --from=build --chown=sforum:sforum /app/apps/web/.output ./.output
COPY --chown=sforum:sforum apps/web/scripts/production-entrypoint.sh /usr/local/bin/sforum-web-entrypoint
RUN chmod 0755 /usr/local/bin/sforum-web-entrypoint
USER sforum
EXPOSE 3000
ENTRYPOINT ["/usr/local/bin/sforum-web-entrypoint"]
CMD ["bun", ".output/server/index.mjs"]
