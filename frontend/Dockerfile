# The bundle hardcodes no API origin — src/api/client.ts fetches relative
# paths like /api/conversations — so there is nothing to bake in at build time
# and no VITE_* build arg is needed. Caddy joins the two halves at runtime.
FROM node:24-alpine AS build

WORKDIR /app

# Manifests first so `npm ci` stays cached until the lockfile actually changes.
COPY package.json package-lock.json ./
RUN npm ci

COPY . .

# `npm run build` is `tsc -b && vite build`, so a type error fails the image
# build rather than shipping a broken bundle.
RUN npm run build

FROM caddy:2-alpine AS runtime

# /usr/share/caddy is the image's default site root; Caddyfile references it
# explicitly anyway so the pairing is readable without knowing the default.
COPY --from=build /app/dist /usr/share/caddy
COPY Caddyfile /etc/caddy/Caddyfile

EXPOSE 80
