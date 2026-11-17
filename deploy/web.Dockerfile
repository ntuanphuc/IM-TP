FROM node:22-alpine AS build
WORKDIR /src
COPY package.json package-lock.json tsconfig.json ./
COPY apps/ apps/
COPY packages/ packages/
RUN npm ci --no-audit --no-fund
RUN npm run build

FROM caddy:2.10-alpine
COPY deploy/Caddyfile /etc/caddy/Caddyfile
COPY --from=build /src/apps/web-chat/dist/ /srv/chat/
COPY --from=build /src/apps/admin-web/dist/ /srv/admin/
