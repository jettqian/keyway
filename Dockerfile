# 三阶段构建：前端 → 后端 → 运行镜像
FROM node:20-alpine AS web
WORKDIR /web
# 国内网络走 npmmirror，规避 npmjs 直连不稳定导致的 npm "Exit handler never called"
COPY web/package.json web/package-lock.json ./
RUN sed -i 's#https://registry.npmjs.org/#https://registry.npmmirror.com/#g' package-lock.json \
 && npm install --no-audit --no-fund \
 && test -x node_modules/.bin/tsc
COPY web/ ./
RUN npm run build && test -f dist/index.html

FROM golang:1.23-alpine AS server
WORKDIR /src
COPY server/go.mod server/go.sum ./
RUN go mod download
COPY server/ ./
# 前端产物拷入内嵌目录（覆盖占位文件）
RUN rm -rf internal/webui/dist
COPY --from=web /web/dist internal/webui/dist
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/keyway ./cmd/keyway

FROM gcr.io/distroless/static:latest
WORKDIR /app
COPY --from=server /out/keyway /app/keyway
ENV KEYWAY_DATA_DIR=/app/data
EXPOSE 8080
VOLUME ["/app/data"]
# 默认 root 保证挂载卷开箱可写；加固部署可 chown 65532 卷目录后加 --user nonroot
ENTRYPOINT ["/app/keyway"]
