FROM node:22-alpine AS web
WORKDIR /web
RUN npm install -g pnpm@12.6.0
COPY web/package.json web/pnpm-lock.yaml web/pnpm-workspace.yaml ./
RUN pnpm install --frozen-lockfile
COPY web/ ./
RUN pnpm build

FROM golang:1.25-alpine AS go
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /web/dist ./web/dist
RUN CGO_ENABLED=0 go build -o /out/jelly-fish ./cmd/jelly-fish

FROM gcr.io/distroless/static:nonroot
COPY --from=go /out/jelly-fish /jelly-fish
ENTRYPOINT ["/jelly-fish"]
