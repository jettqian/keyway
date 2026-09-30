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
# 解包 Go 自带时区库：运行镜像无系统 tzdata，而 Go 的 initLocal 只查
# /usr/share/zoneinfo 目录、不读 ZONEINFO env（后者仅 LoadLocation 生效）
RUN unzip -q /usr/local/go/lib/time/zoneinfo.zip -d /out/zoneinfo

# 运行镜像：scratch + 证书/时区（v1.5.78 起——gcr.io/distroless 在部署网络不可达，
# 且本地无缓存；golang:alpine 阶段已含 ca-certificates 与 Go 自带 zoneinfo.zip，
# 纯静态二进制仅需这两项 + 运行时挂载的 /etc/resolv.conf）
FROM scratch
WORKDIR /app
COPY --from=server /out/keyway /app/keyway
COPY --from=server /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=server /out/zoneinfo /usr/share/zoneinfo
ENV KEYWAY_DATA_DIR=/app/data
EXPOSE 8080
VOLUME ["/app/data"]
# 默认 root 保证挂载卷开箱可写；加固部署可 chown 65532 卷目录后加 --user nonroot
ENTRYPOINT ["/app/keyway"]
