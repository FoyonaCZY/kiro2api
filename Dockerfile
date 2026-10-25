FROM node:22-alpine AS frontend
WORKDIR /src/admin-ui
COPY admin-ui/package.json admin-ui/pnpm-lock.yaml admin-ui/.npmrc admin-ui/pnpm-workspace.yaml ./
RUN npm install -g pnpm && pnpm install --frozen-lockfile
COPY admin-ui ./
RUN pnpm build

FROM golang:1.26-alpine AS backend
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=frontend /src/admin-ui/dist ./admin-ui/dist
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/kiro2api ./cmd/kiro2api

FROM alpine:3.21
LABEL app=kiro2api
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY --from=backend /out/kiro2api /app/kiro2api
VOLUME ["/app/config"]
EXPOSE 8990
CMD ["./kiro2api", "-c", "/app/config/config.json", "--credentials", "/app/config/credentials.json"]
