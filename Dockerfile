# 三阶段构建：前端 → 后端 → 运行镜像
FROM node:20-alpine AS web
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM golang:1.23-alpine AS server
WORKDIR /src
COPY server/go.mod server/go.sum ./
RUN go mod download
COPY server/ ./
# 前端产物拷入内嵌目录（覆盖占位文件）
RUN rm -rf internal/webui/dist && cp -r /web/dist internal/webui/dist
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/keyway ./cmd/keyway

FROM gcr.io/distroless/static:nonroot
WORKDIR /app
COPY --from=server /out/keyway /app/keyway
ENV KEYWAY_DATA_DIR=/app/data
EXPOSE 8080
VOLUME ["/app/data"]
USER nonroot:nonroot
ENTRYPOINT ["/app/keyway"]
