FROM oven/bun:1.3.14-alpine AS build

WORKDIR /app
COPY package.json bun.lock tsconfig.json ./
COPY src ./src
RUN bun install --frozen-lockfile
RUN bun run build

FROM oven/bun:1.3.14-alpine

WORKDIR /app
RUN apk add --no-cache git openssh-client
COPY --from=build /app/dist ./dist

ENV VERSIONHOO_HOST=0.0.0.0
ENV VERSIONHOO_PORT=3000
EXPOSE 3000

CMD ["./dist/app.js"]
